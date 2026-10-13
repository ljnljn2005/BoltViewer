package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func fixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.db")
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucket([]byte("records"))
		if err != nil {
			return err
		}
		for i := 0; i < 125; i++ {
			if err := b.Put([]byte(fmt.Sprintf("key-%03d", i)), []byte(fmt.Sprintf("value %d", i))); err != nil {
				return err
			}
		}
		for key, val := range map[string][]byte{
			"empty":                    {},
			"json":                     []byte(`{"id":9007199254740993,"name":"数据库","nested":{"ok":true}}`),
			"html":                     []byte(`<script>alert("database")</script>`),
			"large":                    bytes.Repeat([]byte("x"), previewLimit+10),
			string([]byte{0xff, 0, 1}): {0, 1, 0xff, 0x80},
		} {
			if err := b.Put([]byte(key), val); err != nil {
				return err
			}
		}
		if err := b.SetSequence(9007199254740993); err != nil {
			return err
		}
		nested, err := b.CreateBucket([]byte("nested"))
		if err != nil {
			return err
		}
		if err := nested.Put([]byte("child"), []byte("子记录")); err != nil {
			return err
		}
		_, err = tx.CreateBucket([]byte{0, 0xff})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func testViewer(t *testing.T) (*viewer, http.Handler, string) {
	t.Helper()
	path := fixture(t)
	v := &viewer{maxUpload: 8 * 1024 * 1024}
	if err := v.open(path, "", false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(v.close)
	return v, v.handler("127.0.0.1:8080"), path
}

func query(path []string, extra map[string]string) string {
	q := url.Values{}
	if path != nil {
		encoded, _ := json.Marshal(path)
		q.Set("path", string(encoded))
	}
	for k, v := range extra {
		q.Set(k, v)
	}
	return "?" + q.Encode()
}

func request(h http.Handler, method, route string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8080"+route, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var data map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestBrowseBinaryNestedAndPagination(t *testing.T) {
	_, h, _ := testViewer(t)
	info := decode(t, request(h, "GET", "/api/info", nil))
	if info["readOnly"] != true || info["buckets"] != float64(2) {
		t.Fatal(info)
	}
	root := decode(t, request(h, "GET", "/api/buckets", nil))["items"].([]any)
	if len(root) != 2 {
		t.Fatal(root)
	}
	path := []string{base64.StdEncoding.EncodeToString([]byte("records"))}
	children := decode(t, request(h, "GET", "/api/buckets"+query(path, nil), nil))["items"].([]any)
	if len(children) != 1 || children[0].(map[string]any)["display"] != "nested" {
		t.Fatal(children)
	}
	first := decode(t, request(h, "GET", "/api/entries"+query(path, map[string]string{"limit": "100"}), nil))
	if first["total"] != float64(131) || first["values"] != float64(130) || first["sequence"] != "9007199254740993" {
		t.Fatal(first)
	}
	if len(first["items"].([]any)) != 100 {
		t.Fatal("page size")
	}
	last := decode(t, request(h, "GET", "/api/entries"+query(path, map[string]string{"offset": "100"}), nil))
	if len(last["items"].([]any)) != 31 {
		t.Fatal("last page size")
	}
	search := decode(t, request(h, "GET", "/api/entries"+query(path, map[string]string{"q": "KEY-12"}), nil))
	if search["matched"] != float64(5) {
		t.Fatal(search)
	}
	binarySearch := decode(t, request(h, "GET", "/api/entries"+query(path, map[string]string{"q": "ff0001"}), nil))
	if binarySearch["matched"] != float64(1) {
		t.Fatal(binarySearch)
	}
	nested := append(path, base64.StdEncoding.EncodeToString([]byte("nested")))
	child := decode(t, request(h, "GET", "/api/entries"+query(nested, nil), nil))
	if child["total"] != float64(1) {
		t.Fatal(child)
	}
}

func TestValuesAndLosslessExport(t *testing.T) {
	_, h, _ := testViewer(t)
	path := []string{base64.StdEncoding.EncodeToString([]byte("records"))}
	for _, tc := range []struct {
		key       string
		format    string
		size      int
		truncated bool
	}{
		{"empty", "text", 0, false}, {"json", "json", len([]byte(`{"id":9007199254740993,"name":"数据库","nested":{"ok":true}}`)), false},
		{string([]byte{0xff, 0, 1}), "binary", 4, false}, {"large", "text", previewLimit + 10, true},
	} {
		t.Run(tc.format+tc.key, func(t *testing.T) {
			key := base64.StdEncoding.EncodeToString([]byte(tc.key))
			val := decode(t, request(h, "GET", "/api/value"+query(path, map[string]string{"key": key}), nil))
			if val["format"] != tc.format || val["size"] != float64(tc.size) || val["truncated"] != tc.truncated {
				t.Fatal(val["format"], val["size"], val["truncated"])
			}
			preview, err := base64.StdEncoding.DecodeString(val["data"].(string))
			if err != nil {
				t.Fatal(err)
			}
			if tc.truncated && len(preview) != previewLimit {
				t.Fatal("preview size")
			}
			download := request(h, "GET", "/api/value"+query(path, map[string]string{"key": key, "download": "1"}), nil)
			if download.Code != 200 || download.Body.Len() != tc.size {
				t.Fatal("download size")
			}
			if !tc.truncated && !bytes.Equal(preview, download.Body.Bytes()) {
				t.Fatal("value bytes changed")
			}
		})
	}
	export := decode(t, request(h, "GET", "/api/export"+query(path, nil), nil))
	entries := export["entries"].([]any)
	if len(entries) != 131 || export["format"] != "boltdb-viewer/v1" {
		t.Fatal("export incomplete")
	}
	foundBinary, foundNested, foundLarge := false, false, false
	for _, raw := range entries {
		item := raw.(map[string]any)
		key, _ := base64.StdEncoding.DecodeString(item["key_base64"].(string))
		switch string(key) {
		case string([]byte{0xff, 0, 1}):
			val, _ := base64.StdEncoding.DecodeString(item["value_base64"].(string))
			foundBinary = bytes.Equal(val, []byte{0, 1, 0xff, 0x80})
		case "nested":
			foundNested = len(item["entries"].([]any)) == 1
		case "large":
			val, _ := base64.StdEncoding.DecodeString(item["value_base64"].(string))
			foundLarge = len(val) == previewLimit+10
		}
	}
	if !foundBinary || !foundNested || !foundLarge {
		t.Fatal("lossless export failed")
	}
}

func TestReadOnlyAndFailedSwitch(t *testing.T) {
	v, h, path := testViewer(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.db.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("records")).Put([]byte("changed"), []byte("bad")) }); err != bolt.ErrDatabaseReadOnly {
		t.Fatalf("unexpected write result: %v", err)
	}
	invalid := filepath.Join(t.TempDir(), "invalid.db")
	if err := os.WriteFile(invalid, bytes.Repeat([]byte("x"), 8192), 0600); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"path": invalid})
	if w := request(h, "POST", "/api/open", body); w.Code != 400 {
		t.Fatal(w.Code)
	}
	info := decode(t, request(h, "GET", "/api/info", nil))
	if info["path"] != path {
		t.Fatal("failed open replaced current database")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("original database was modified")
	}
}

func TestUploadAndCleanup(t *testing.T) {
	v, h, path := testViewer(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "uploaded.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:8080/api/upload", &body)
	r.Header.Set("Content-Type", w.FormDataContentType())
	response := httptest.NewRecorder()
	h.ServeHTTP(response, r)
	decode(t, response)
	uploaded := v.path
	if !v.temporary || v.name != "uploaded.db" {
		t.Fatal("upload not active")
	}
	info := decode(t, request(h, "GET", "/api/info", nil))
	if info["path"] != "上传的只读副本" {
		t.Fatal("temporary path exposed")
	}
	if err := v.open(path, "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(uploaded); !os.IsNotExist(err) {
		t.Fatal("temporary database not removed")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("original removed")
	}
}

func TestRequestValidation(t *testing.T) {
	_, h, _ := testViewer(t)
	for _, tc := range []struct{ host, origin, site string }{
		{"evil.example:8080", "", ""}, {"127.0.0.1:1234", "", ""},
		{"127.0.0.1:8080", "https://evil.example", ""}, {"localhost:8080", "", "cross-site"},
	} {
		r := httptest.NewRequest("GET", "http://"+tc.host+"/api/info", nil)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Sec-Fetch-Site", tc.site)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(tc, w.Code)
		}
	}
	for _, route := range []string{
		"/api/entries?path=invalid", "/api/entries?path=%5B%22%25%22%5D", "/api/entries", "/api/value?key=invalid",
		"/api/value" + query([]string{base64.StdEncoding.EncodeToString([]byte("records"))}, map[string]string{"key": base64.StdEncoding.EncodeToString([]byte("missing"))}),
	} {
		if w := request(h, "GET", route, nil); w.Code != 400 {
			t.Fatal(route, w.Code)
		}
	}
	index := request(h, "GET", "/", nil)
	if index.Code != 200 || !strings.Contains(index.Body.String(), "BoltDB Viewer") {
		t.Fatal("embedded frontend missing")
	}
	if index.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatal("headers missing")
	}
}

func TestNoDatabase(t *testing.T) {
	v := &viewer{maxUpload: 1024}
	h := v.handler("127.0.0.1:8080")
	if data := decode(t, request(h, "GET", "/api/info", nil)); data["open"] != false {
		t.Fatal(data)
	}
	if w := request(h, "GET", "/api/buckets", nil); w.Code != 409 {
		t.Fatal(w.Code)
	}
}
