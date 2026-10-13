package main

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
)

//go:embed web/*
var assets embed.FS

const previewLimit = 1024 * 1024

type viewer struct {
	mu        sync.RWMutex
	db        *bolt.DB
	path      string
	name      string
	temporary bool
	maxUpload int64
}

func main() {
	dbPath := flag.String("db", "database.db", "database file to open (read-only)")
	addr := flag.String("addr", "127.0.0.1:8080", "local HTTP listen address")
	launch := flag.Bool("open", true, "open the browser on startup")
	uploadMB := flag.Int64("max-upload-mb", 512, "maximum uploaded database size in MiB")
	flag.Parse()
	if *uploadMB < 1 || *uploadMB > 16384 {
		log.Fatal("max-upload-mb must be between 1 and 16384")
	}
	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		log.Fatal(err)
	}
	if host != "localhost" && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback()) {
		log.Fatal("the viewer must listen on a loopback address, e.g. 127.0.0.1:8080")
	}
	v := &viewer{maxUpload: *uploadMB * 1024 * 1024}
	defer v.close()
	if *dbPath != "" {
		if err := v.open(*dbPath, "", false); err != nil {
			log.Printf("Database not opened: %v (you can open one in the browser)", err)
		}
	}
	listener, err := net.Listen("tcp", *addr)
	addressSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "addr" {
			addressSet = true
		}
	})
	if err != nil && !addressSet {
		// Keep a double-click launch usable when another application owns port 8080.
		listener, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		log.Fatal(err)
	}
	url := "http://" + listener.Addr().String()
	server := &http.Server{Handler: v.handler(listener.Addr().String()), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	log.Printf("BoltDB Viewer: %s | Read-only | Ctrl+C to stop", url)
	if *launch {
		go openBrowser(url)
	}
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Print(err)
	}
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Run(); err != nil {
		log.Printf("Open %s in your browser: %v", url, err)
	}
}

func (v *viewer) open(path, name string, temporary bool) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("请选择普通数据库文件")
	}
	if info.Size() < 8192 {
		return errors.New("文件过小，不是有效的 BoltDB 数据库")
	}
	// Serialize switching so the old handle can never be closed during a read.
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.db != nil && v.path == absolute {
		return nil
	}
	db, err := bolt.Open(absolute, 0600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return fmt.Errorf("无法打开数据库（请确认格式正确，且没有其他程序独占文件）: %w", err)
	}
	if name == "" {
		name = filepath.Base(absolute)
	}
	v.closeLocked()
	v.db, v.path, v.name, v.temporary = db, absolute, name, temporary
	return nil
}

func (v *viewer) closeLocked() {
	if v.db != nil {
		_ = v.db.Close()
	}
	if v.temporary {
		_ = os.Remove(v.path)
	}
	v.db, v.path, v.name, v.temporary = nil, "", "", false
}

func (v *viewer) close() { v.mu.Lock(); defer v.mu.Unlock(); v.closeLocked() }

func (v *viewer) handler(address string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/info", v.info)
	mux.HandleFunc("POST /api/open", v.openPath)
	mux.HandleFunc("POST /api/upload", v.upload)
	mux.HandleFunc("GET /api/buckets", v.buckets)
	mux.HandleFunc("GET /api/entries", v.entries)
	mux.HandleFunc("GET /api/value", v.value)
	mux.HandleFunc("GET /api/export", v.export)
	web, _ := fs.Sub(assets, "web")
	mux.Handle("/", http.FileServer(http.FS(web)))
	_, port, _ := net.SplitHostPort(address)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Local-only host and origin checks prevent websites from reading local files via this API.
		host, requestPort, err := net.SplitHostPort(r.Host)
		ip := net.ParseIP(host)
		if err != nil || requestPort != port || (host != "localhost" && (ip == nil || !ip.IsLoopback())) {
			fail(w, 403, "无效的本地请求地址")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			fail(w, 403, "仅允许本地页面访问")
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" {
			fail(w, 403, "仅允许本地页面访问")
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
		w.Header().Set("Cache-Control", "no-store")
		defer func() {
			if p := recover(); p != nil {
				log.Printf("Database request failed: %v", p)
				fail(w, 500, "读取失败，数据库可能已损坏")
			}
		}()
		mux.ServeHTTP(w, r)
	})
}

func respond(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
func fail(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]string{"error": message})
}

func (v *viewer) read(w http.ResponseWriter, fn func(*bolt.Tx) error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.db == nil {
		fail(w, 409, "请先打开一个数据库")
		return
	}
	if err := v.db.View(fn); err != nil {
		fail(w, 400, err.Error())
	}
}

func decodePath(r *http.Request) ([][]byte, error) {
	raw := r.URL.Query().Get("path")
	if raw == "" {
		return nil, nil
	}
	var parts []string
	if len(raw) > 65536 {
		return nil, errors.New("bucket 路径过长")
	}
	if err := json.Unmarshal([]byte(raw), &parts); err != nil {
		return nil, errors.New("无效的 bucket 路径")
	}
	if len(parts) > 256 {
		return nil, errors.New("bucket 层级过深")
	}
	path := make([][]byte, len(parts))
	for i, s := range parts {
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil || len(b) == 0 {
			return nil, errors.New("无效的 bucket 名称")
		}
		path[i] = b
	}
	return path, nil
}

func findBucket(tx *bolt.Tx, path [][]byte) (*bolt.Bucket, error) {
	if len(path) == 0 {
		return nil, errors.New("请选择一个 bucket")
	}
	b := tx.Bucket(path[0])
	for _, p := range path[1:] {
		if b == nil {
			break
		}
		b = b.Bucket(p)
	}
	if b == nil {
		return nil, errors.New("bucket 不存在，请刷新数据库")
	}
	return b, nil
}

func printable(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}
	for _, r := range string(b) {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}

func display(b []byte) string {
	if printable(b) {
		return string(b)
	}
	return "0x" + hex.EncodeToString(b)
}

func preview(b []byte) string {
	if len(b) <= 160 {
		return display(b)
	}
	end := 160
	if utf8.Valid(b) {
		for end > 0 && !utf8.Valid(b[:end]) {
			end--
		}
	}
	return display(b[:end]) + "…"
}

func (v *viewer) info(w http.ResponseWriter, r *http.Request) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.db == nil {
		respond(w, 200, map[string]any{"open": false, "maxUpload": v.maxUpload})
		return
	}
	info, err := os.Stat(v.path)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	count := 0
	err = v.db.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(_ []byte, _ *bolt.Bucket) error { count++; return nil })
	})
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	path := v.path
	if v.temporary {
		path = "上传的只读副本"
	}
	respond(w, 200, map[string]any{"open": true, "name": v.name, "path": path, "size": info.Size(), "pageSize": v.db.Info().PageSize, "buckets": count, "readOnly": true, "uploaded": v.temporary, "maxUpload": v.maxUpload})
}

func (v *viewer) openPath(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Path string `json:"path"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil || strings.TrimSpace(input.Path) == "" {
		fail(w, 400, "请输入数据库文件的本地路径")
		return
	}
	if err := v.open(strings.TrimSpace(input.Path), "", false); err != nil {
		fail(w, 400, err.Error())
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}

func (v *viewer) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, v.maxUpload+1024*1024)
	if err := r.ParseMultipartForm(8 * 1024 * 1024); err != nil {
		fail(w, 400, "上传失败，请检查文件和大小限制")
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, header, err := r.FormFile("file")
	if err != nil {
		fail(w, 400, "请选择数据库文件")
		return
	}
	defer file.Close()
	if header.Size > v.maxUpload {
		fail(w, 413, "文件超过上传大小限制，请使用本地路径打开")
		return
	}
	temp, err := os.CreateTemp("", "boltdb-viewer-*.db")
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	path := temp.Name()
	keep := false
	defer func() {
		_ = temp.Close()
		if !keep {
			_ = os.Remove(path)
		}
	}()
	n, err := io.Copy(temp, io.LimitReader(file, v.maxUpload+1))
	if err != nil {
		fail(w, 400, "写入上传文件失败")
		return
	}
	if n > v.maxUpload {
		fail(w, 413, "文件超过上传大小限制")
		return
	}
	if err = temp.Close(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	if err = v.open(path, filepath.Base(header.Filename), true); err != nil {
		fail(w, 400, err.Error())
		return
	}
	keep = true
	respond(w, 200, map[string]bool{"ok": true})
}

type bucketItem struct {
	Name    string `json:"name"`
	Display string `json:"display"`
}

func (v *viewer) buckets(w http.ResponseWriter, r *http.Request) {
	path, err := decodePath(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	v.read(w, func(tx *bolt.Tx) error {
		items := make([]bucketItem, 0)
		appendBucket := func(k []byte, _ *bolt.Bucket) error {
			items = append(items, bucketItem{base64.StdEncoding.EncodeToString(k), display(k)})
			return nil
		}
		if len(path) == 0 {
			if err := tx.ForEach(appendBucket); err != nil {
				return err
			}
		} else {
			b, err := findBucket(tx, path)
			if err != nil {
				return err
			}
			if err := b.ForEachBucket(func(k []byte) error { return appendBucket(k, nil) }); err != nil {
				return err
			}
		}
		respond(w, 200, map[string]any{"items": items})
		return nil
	})
}

type entryItem struct {
	Key     string `json:"key"`
	Display string `json:"display"`
	Kind    string `json:"kind"`
	Size    int    `json:"size"`
	Preview string `json:"preview"`
	Format  string `json:"format"`
}

func valueFormat(b []byte) string {
	if !printable(b) {
		return "binary"
	}
	if json.Valid(b) {
		return "json"
	}
	return "text"
}

func (v *viewer) entries(w http.ResponseWriter, r *http.Request) {
	path, err := decodePath(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 500 {
		limit = 100
	}
	query := strings.ToLower(r.URL.Query().Get("q"))
	v.read(w, func(tx *bolt.Tx) error {
		b, err := findBucket(tx, path)
		if err != nil {
			return err
		}
		items := make([]entryItem, 0, limit)
		total, matched, values, children := 0, 0, 0, 0
		c := b.Cursor()
		for k, val := c.First(); k != nil; k, val = c.Next() {
			if err := r.Context().Err(); err != nil {
				return err
			}
			total++
			if val == nil {
				children++
			} else {
				values++
			}
			if query != "" && !strings.Contains(strings.ToLower(display(k)), query) && !strings.Contains(hex.EncodeToString(k), query) {
				continue
			}
			matched++
			if matched <= offset || len(items) >= limit {
				continue
			}
			item := entryItem{Key: base64.StdEncoding.EncodeToString(k), Display: display(k), Kind: "value", Size: len(val), Preview: preview(val), Format: valueFormat(val)}
			if val == nil {
				item.Kind, item.Format, item.Preview = "bucket", "bucket", "嵌套 bucket"
			}
			items = append(items, item)
		}
		respond(w, 200, map[string]any{"items": items, "total": total, "matched": matched, "values": values, "buckets": children, "offset": offset, "limit": limit, "sequence": strconv.FormatUint(b.Sequence(), 10)})
		return nil
	})
}

func (v *viewer) value(w http.ResponseWriter, r *http.Request) {
	path, err := decodePath(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	key, err := base64.StdEncoding.DecodeString(r.URL.Query().Get("key"))
	if err != nil || len(key) == 0 {
		fail(w, 400, "无效的键")
		return
	}
	v.read(w, func(tx *bolt.Tx) error {
		b, err := findBucket(tx, path)
		if err != nil {
			return err
		}
		if b.Bucket(key) != nil {
			return errors.New("此键是一个嵌套 bucket")
		}
		val := b.Get(key)
		if val == nil {
			return errors.New("键不存在，请刷新数据库")
		}
		if r.URL.Query().Get("download") == "1" {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename="value.bin"`)
			w.Header().Set("Content-Length", strconv.Itoa(len(val)))
			_, err := w.Write(val)
			return err
		}
		truncated := len(val) > previewLimit
		shown := val
		if truncated {
			shown = val[:previewLimit]
		}
		text := ""
		if utf8.Valid(shown) {
			text = string(shown)
		} else if utf8.Valid(val) {
			end := len(shown)
			for end > 0 && !utf8.Valid(shown[:end]) {
				end--
			}
			text = string(shown[:end])
		}
		respond(w, 200, map[string]any{"key": base64.StdEncoding.EncodeToString(key), "keyDisplay": display(key), "keySize": len(key), "size": len(val), "format": valueFormat(val), "data": base64.StdEncoding.EncodeToString(shown), "text": text, "truncated": truncated, "previewLimit": previewLimit})
		return nil
	})
}

// Streaming export preserves arbitrary binary names, keys and values in base64.
func writeEntries(ctx context.Context, w io.Writer, b *bolt.Bucket, depth int) error {
	if depth > 256 {
		return errors.New("bucket 层级超过导出限制")
	}
	if _, err := io.WriteString(w, "["); err != nil {
		return err
	}
	c := b.Cursor()
	first := true
	for k, val := c.First(); k != nil; k, val = c.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !first {
			if _, err := io.WriteString(w, ","); err != nil {
				return err
			}
		}
		first = false
		key, _ := json.Marshal(base64.StdEncoding.EncodeToString(k))
		if _, err := fmt.Fprintf(w, "{\"key_base64\":%s,", key); err != nil {
			return err
		}
		if val == nil {
			if _, err := io.WriteString(w, "\"entries\":"); err != nil {
				return err
			}
			if err := writeEntries(ctx, w, b.Bucket(k), depth+1); err != nil {
				return err
			}
		} else {
			if _, err := io.WriteString(w, "\"value_base64\":\""); err != nil {
				return err
			}
			enc := base64.NewEncoder(base64.StdEncoding, w)
			if _, err := enc.Write(val); err != nil {
				return err
			}
			if err := enc.Close(); err != nil {
				return err
			}
			if _, err := io.WriteString(w, "\""); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(w, "}"); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "]")
	return err
}

func (v *viewer) export(w http.ResponseWriter, r *http.Request) {
	path, err := decodePath(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	v.read(w, func(tx *bolt.Tx) error {
		b, err := findBucket(tx, path)
		if err != nil {
			return err
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="bucket-export.json"`)
		if _, err := io.WriteString(w, "{\"format\":\"boltdb-viewer/v1\",\"entries\":"); err != nil {
			return err
		}
		if err := writeEntries(r.Context(), w, b, 0); err != nil {
			return err
		}
		_, err = io.WriteString(w, "}")
		return err
	})
}
