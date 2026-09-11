// The lab is a disposable local API for exercising contract discovery.
package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

//go:embed openapi.yaml
var openapi []byte

type lab struct {
	mu      sync.Mutex
	next    int
	objects map[string]map[string]any
}

func main() {
	address := flag.String("listen", "127.0.0.1:8089", "Lab listen address")
	flag.Parse()
	state := &lab{objects: map[string]map[string]any{}}
	server := &http.Server{Addr: *address, Handler: state, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	log.Printf("Disposable widget lab: http://%s (spec at /openapi.yaml)", *address)
	log.Fatal(server.ListenAndServe())
}
func (l *lab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r.URL.Path == "/openapi.yaml" {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(openapi)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/widgets" && r.Method == "POST" {
		var body map[string]any
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body) != nil || !valid(body) {
			reject(w)
			return
		}
		l.next++
		id := fmt.Sprint(l.next)
		body["id"] = id
		body["name"] = strings.TrimSpace(body["name"].(string))
		if _, ok := body["mode"]; !ok {
			body["mode"] = "simple"
		}
		l.objects[id] = body
		w.Header().Set("Location", "/widgets/"+id)
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(body)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/widgets/") {
		http.NotFound(w, r)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/widgets/")
	object, ok := l.objects[id]
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case "GET":
		_ = json.NewEncoder(w).Encode(object)
	case "DELETE":
		delete(l.objects, id)
		w.WriteHeader(204)
	case "PATCH":
		var patch map[string]any
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&patch) != nil || patch == nil {
			reject(w)
			return
		}
		merged := map[string]any{}
		for k, v := range object {
			merged[k] = v
		}
		for k, v := range patch {
			merged[k] = v
		}
		if !valid(merged) {
			reject(w)
			return
		}
		merged["name"] = strings.TrimSpace(merged["name"].(string))
		l.objects[id] = merged
		_ = json.NewEncoder(w).Encode(merged)
	default:
		w.WriteHeader(405)
	}
}
func valid(body map[string]any) bool {
	if body == nil {
		return false
	}
	name, ok := body["name"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return false
	}
	mode := "simple"
	if v, exists := body["mode"]; exists {
		var ok bool
		mode, ok = v.(string)
		if !ok || mode != "simple" && mode != "advanced" {
			return false
		}
	}
	_, cert := body["certificate"]
	_, password := body["certificatePassword"]
	if mode == "advanced" && !cert || mode == "simple" && cert || cert && !password {
		return false
	}
	for _, name := range []string{"certificate", "certificatePassword"} {
		if value, exists := body[name]; exists {
			if _, ok := value.(string); !ok {
				return false
			}
		}
	}
	return true
}
func reject(w http.ResponseWriter) {
	w.WriteHeader(422)
	_, _ = w.Write([]byte(`{"code":"InvalidRequest","message":"field validation failed"}`))
}
