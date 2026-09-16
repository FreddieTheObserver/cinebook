package web

import (
	"net/http"
	"net/http/httptest"
	"path"
	"regexp"
	"strings"
	"testing"
)

func serve(t *testing.T, method, target string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)
	return rec
}

func TestServesThePageAtTheRoot(t *testing.T) {
	rec := serve(t, http.MethodGet, "/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	for name, want := range map[string]string{
		"Content-Type":            "text/html; charset=utf-8",
		"Content-Security-Policy": contentSecurityPolicy,
		"Referrer-Policy":         "no-referrer",
		"X-Content-Type-Options":  "nosniff",
		"Cache-Control":           "no-cache",
	} {
		if got := rec.Header().Get(name); got != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("no ETag, so every load would download the page again")
	}
	if !strings.Contains(rec.Body.String(), `<script type="module" src="/js/app.js">`) {
		t.Error("the page does not load the app")
	}
}

func TestUnchangedAssetsAnswerNotModified(t *testing.T) {
	first := serve(t, http.MethodGet, "/js/app.js", nil)
	again := serve(t, http.MethodGet, "/js/app.js", map[string]string{"If-None-Match": first.Header().Get("ETag")})
	if again.Code != http.StatusNotModified || again.Body.Len() != 0 {
		t.Fatalf("got %d with %d bytes, want an empty 304", again.Code, again.Body.Len())
	}
	if serve(t, http.MethodGet, "/style.css", nil).Header().Get("ETag") == first.Header().Get("ETag") {
		t.Fatal("different files share an ETag")
	}
}

func TestContentTypes(t *testing.T) {
	for target, want := range map[string]string{
		"/style.css":         "text/css",
		"/js/app.js":         "javascript",
		"/js/views/seats.js": "javascript",
		"/favicon.svg":       "image/svg+xml",
	} {
		if got := serve(t, http.MethodGet, target, nil).Header().Get("Content-Type"); !strings.Contains(got, want) {
			t.Errorf("%s: got Content-Type %q, want %s", target, got, want)
		}
	}
}

func TestOnlyEmbeddedFilesAreServed(t *testing.T) {
	for _, target := range []string{"/index.html", "/static/style.css", "/js", "/js/", "/nope", "/web.go", "/v1/movies"} {
		if code := serve(t, http.MethodGet, target, nil).Code; code != http.StatusNotFound {
			t.Errorf("%s: got %d, want 404", target, code)
		}
	}
}

func TestOnlyReadMethods(t *testing.T) {
	rec := serve(t, http.MethodPost, "/", nil)
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST: got %d, Allow %q", rec.Code, rec.Header().Get("Allow"))
	}
	if head := serve(t, http.MethodHead, "/", nil); head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD: got %d with %d bytes", head.Code, head.Body.Len())
	}
}

var (
	moduleImport = regexp.MustCompile(`(?m)^\s*(?:import|export)\b[^;]*?["'](\.{1,2}/[^"']+)["']`)
	pageRef      = regexp.MustCompile(`(?:src|href)="(/[^"]*)"`)
)

// With no JavaScript toolchain in the build, a mistyped import would only show
// up as a blank page in a browser.
func TestEveryModuleResolvesAndIsReachable(t *testing.T) {
	assets := loadAssets()

	for _, m := range pageRef.FindAllStringSubmatch(string(assets["/"].content), -1) {
		if _, ok := assets[m[1]]; !ok {
			t.Errorf("index.html references %s, which is not embedded", m[1])
		}
	}

	reached := map[string]bool{}
	queue := []string{"/js/app.js"}
	for len(queue) > 0 {
		file := queue[0]
		queue = queue[1:]
		if reached[file] {
			continue
		}
		reached[file] = true
		a, ok := assets[file]
		if !ok {
			t.Errorf("module %s is imported but not embedded", file)
			continue
		}
		for _, m := range moduleImport.FindAllStringSubmatch(string(a.content), -1) {
			queue = append(queue, path.Join(path.Dir(file), m[1]))
		}
	}

	for name := range assets {
		if strings.HasSuffix(name, ".js") && !reached[name] {
			t.Errorf("module %s is embedded but nothing imports it", name)
		}
	}
	if len(reached) < 2 {
		t.Fatalf("import scan found nothing beyond app.js: %v", reached)
	}
}

// The policy forbids inline script and style, and the views build DOM through
// text and attributes only, so markup sinks would be either blocked or unsafe.
func TestPagesStayWithinTheContentSecurityPolicy(t *testing.T) {
	assets := loadAssets()

	page := string(assets["/"].content)
	if regexp.MustCompile(`<script(?:\s+type="module")?>`).MatchString(page) || strings.Contains(page, "style=") || strings.Contains(page, "<style") {
		t.Error("index.html has inline script or style")
	}

	sinks := regexp.MustCompile(`\b(innerHTML|outerHTML|insertAdjacentHTML|document\.write|eval)\b|new Function|setAttribute\(\s*["']style`)
	for name, a := range assets {
		if strings.HasSuffix(name, ".js") {
			if m := sinks.FindString(string(a.content)); m != "" {
				t.Errorf("%s uses %s", name, m)
			}
		}
	}
}
