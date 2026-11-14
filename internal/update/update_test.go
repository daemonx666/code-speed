package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v0.5.0", "v0.4.9", true}, {"v0.4.0", "v0.4.0", false}, {"v0.4.0", "v0.4.0-dev", true},
		{"v1.0.0", "dev", true}, {"garbage", "v0.1.0", false}, {"v0.3.9", "v0.4.0-dev", false}, {"v0.10.0", "v0.9.0", true},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestLatest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/"+Repo+"/releases/latest" || r.Header.Get("User-Agent") != "code-speed" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"tag_name":"v0.6.0","html_url":"https://github.com/daemonx666/code-speed/releases/tag/v0.6.0"}`))
	}))
	defer srv.Close()
	API = srv.URL
	r, err := Latest(context.Background(), "v0.5.0")
	if err != nil || !r.Newer || r.Version != "v0.6.0" {
		t.Fatalf("%+v %v", r, err)
	}
}
