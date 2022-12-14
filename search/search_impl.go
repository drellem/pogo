package main

import (
	"encoding/gob"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"

	pogoPlugin "github.com/marginalia-gaming/pogo/plugin"
)

const pogoDir = ".pogo"
const searchDir = "search"

// API Version for this plugin
const version = "0.0.1"

type BasicSearch struct {
	logger   hclog.Logger
	projects map[string]IndexedProject
	watcher  *fsnotify.Watcher
}

// Input to an "Execute" call should be a serialized SearchRequest
type SearchRequest struct {
	// Currently only type is "files"
	Type        string `json:"type"`
	ProjectRoot string `json:"projectRoot"`
}

type SearchResponse struct {
	Index IndexedProject `json:"index"`
	Error string         `json:"error"`
}

type ErrorResponse struct {
	ErrorCode int    `json:"errorCode"`
	Error     string `json:"error"`
}

func (g *BasicSearch) errorResponse(code int, message string) string {
	resp := ErrorResponse{ErrorCode: code, Error: message}
	bytes, err := json.Marshal(&resp)
	if err != nil {
		g.logger.Error("Error writing error response")
		panic(err)
	}
	return url.QueryEscape(string(bytes))
}

func (g *BasicSearch) searchResponse(index *IndexedProject) string {
	var response SearchResponse
	if index == nil {
		indexedProject := IndexedProject{Root: "", Paths: []string{}}
		response.Index = indexedProject
		response.Error = "No results. See logs for further details."
	} else {
		response.Index = *index
		response.Error = ""
	}

	bytes, err := json.Marshal(&response)
	if err != nil {
		g.logger.Error("Error writing search response")
		return g.errorResponse(500, "Error writing search response")
	}
	return url.QueryEscape(string(bytes))
}

func (g *BasicSearch) Info() *pogoPlugin.PluginInfoRes {
	g.logger.Debug("Returning version %s", version)
	return &pogoPlugin.PluginInfoRes{Version: version}
}

// Just a dummy function for now
func (g *BasicSearch) Execute(encodedReq string) string {
	g.logger.Debug("Executing request.")
	req, err2 := url.QueryUnescape(encodedReq)
	if err2 != nil {
		g.logger.Error("500 Could not query decode request.", "error", err2)
		return g.errorResponse(500, "Could not query decode request.")
	}
	var searchRequest SearchRequest
	err := json.Unmarshal([]byte(req), &searchRequest)
	if err != nil {
		g.logger.Info("400 Invalid request.", "error", err)
		return g.errorResponse(400, "Invalid request.")
	}
	if searchRequest.Type != "files" {
		g.logger.Info("404 Unknown request type.", "type", searchRequest.Type)
		return g.errorResponse(404, "Unknown request type.")
	}

	proj, err3 := g.GetFiles(searchRequest.ProjectRoot)
	if err3 != nil {
		g.logger.Error("500 Error retrieving files.", "error", err3)
		return g.errorResponse(500, "Error retrieving files.")
	}
	return g.searchResponse(proj)
}

func (g *BasicSearch) ProcessProject(req *pogoPlugin.IProcessProjectReq) error {
	g.logger.Debug("Processing project %s", (*req).Path())
	go g.Index(req)
	return nil
}

// handshakeConfigs are used to just do a basic handshake betw1een
// a plugin and host. If the handshake fails, a user friendly error is shown.
// This prevents users from executing bad plugins or executing a plugin
// directory. It is a UX feature, not a security feature.
var handshakeConfig = plugin.HandshakeConfig{
	ProtocolVersion:  2,
	MagicCookieKey:   "SEARCH_PLUGIN",
	MagicCookieValue: "93f6bc9f97c03ed00fa85c904aca15a92752e549",
}

// Ensure's plugin directory exists in project config
// Returns full path of search dir
func makeSearchDir(path string) string {
	fullSearchDir := filepath.Join(path, pogoDir, searchDir)
	err := os.MkdirAll(fullSearchDir, os.ModePerm)
	if err != nil {
		panic("Could not create search directory. Exiting.")
	}
	return fullSearchDir
}

func createBasicSearch() *BasicSearch {
	logger := hclog.New(&hclog.LoggerOptions{
		Level:      hclog.Trace,
		Output:     os.Stderr,
		JSONFormat: true,
	})

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		logger.Error("Could not create file watcher. Index will run frequently.")
	}

	basicSearch := &BasicSearch{
		logger:   logger,
		projects: make(map[string]IndexedProject),
		watcher:  watcher,
	}

	if watcher != nil {
		go func() {
			for {
				select {
				case event, ok := <-watcher.Events:
					if !ok {
						logger.Warn("Not ok")
						return
					}
					if event.Has(fsnotify.Create) || event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
						logger.Info("File update: ", event)
						basicSearch.ReIndex(event.Name)
					}
				case err, ok := <-watcher.Errors:

					if !ok {
						return
					}
					logger.Error("File watcher error: %v", err)
				}
			}
		}()
	}
	return basicSearch
}

func main() {
	gob.Register(pogoPlugin.ProcessProjectReq{})

	basicSearch := createBasicSearch()
	defer basicSearch.watcher.Close()

	// pluginMap is the map of plugins we can dispense.
	var pluginMap = map[string]plugin.Plugin{
		"basicSearch": &pogoPlugin.PogoPlugin{Impl: basicSearch},
	}

	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: handshakeConfig,
		Plugins:         pluginMap,
	})
}
