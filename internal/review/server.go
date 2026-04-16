package review

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/colton/video-archive/internal/config"
	"github.com/colton/video-archive/internal/store"
)

// Server is the review UI HTTP server.
type Server struct {
	cfg  config.Config
	db   *store.DB
	mux  *http.ServeMux
	port int
}

// New creates a review server.
func New(cfg config.Config, db *store.DB, port int) *Server {
	s := &Server{cfg: cfg, db: db, port: port}
	s.mux = http.NewServeMux()
	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	// Static file serving for crops, frames, and master videos
	s.mux.Handle("/static/crops/", http.StripPrefix("/static/crops/",
		http.FileServer(http.Dir(s.cfg.CropsDir()))))
	s.mux.Handle("/static/frames/", http.StripPrefix("/static/frames/",
		http.FileServer(http.Dir(s.cfg.FramesDir()))))
	s.mux.Handle("/static/master/", http.StripPrefix("/static/master/",
		http.FileServer(http.Dir(s.cfg.MastersDir()))))

	// Pages
	s.mux.HandleFunc("GET /review/{recordingID}", s.handleReview)
	s.mux.HandleFunc("GET /identities", s.handleIdentities)
	s.mux.HandleFunc("GET /identities/{id}", s.handleIdentityDetail)
	s.mux.HandleFunc("GET /scenes/{recordingID}", s.handleScenes)
	s.mux.HandleFunc("GET /groups", s.handleGroups)
	s.mux.HandleFunc("GET /{$}", s.handleHome)

	// HTMX actions — clusters
	s.mux.HandleFunc("POST /review/{recordingID}/clusters/{clusterID}/name", s.handleNameCluster)
	s.mux.HandleFunc("POST /review/{recordingID}/clusters/{clusterID}/reject", s.handleRejectCluster)
	s.mux.HandleFunc("POST /review/{recordingID}/clusters/merge", s.handleMergeClusters)

	// HTMX actions — identities
	s.mux.HandleFunc("GET /identities/{id}/rename-confirm", s.handleIdentityRenameConfirm)
	s.mux.HandleFunc("POST /identities/{id}/rename", s.handleIdentityRename)
	s.mux.HandleFunc("POST /identities/{id}/delete", s.handleIdentityDelete)
	s.mux.HandleFunc("POST /identities/{id}/clusters/{clusterID}/detach", s.handleClusterDetach)
	s.mux.HandleFunc("POST /identities/merge", s.handleIdentityMerge)

	// HTMX actions — groups
	s.mux.HandleFunc("POST /groups/create", s.handleGroupCreate)
	s.mux.HandleFunc("POST /groups/{id}/rename", s.handleGroupRename)
	s.mux.HandleFunc("POST /groups/{id}/delete", s.handleGroupDelete)
	s.mux.HandleFunc("POST /groups/{id}/add", s.handleGroupAddMember)
	s.mux.HandleFunc("POST /groups/{id}/remove/{identityID}", s.handleGroupRemoveMember)
}

// ListenAndServe starts the HTTP server.
func (s *Server) ListenAndServe() error {
	addr := fmt.Sprintf("127.0.0.1:%d", s.port)
	slog.Info("review server starting", "addr", "http://"+addr)
	return http.ListenAndServe(addr, s.mux)
}

// URL returns the URL to open in a browser.
func (s *Server) URL(recordingID int64) string {
	return fmt.Sprintf("http://127.0.0.1:%d/review/%d", s.port, recordingID)
}
