package server

import (
	"context"
	"errors"
	"html/template"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jholdstock/dcrmapper/crawler"
)

var amgr *crawler.Manager

func NewRouter() *gin.Engine {
	// With release mode enabled, gin will only read template files once and cache them.
	// With release mode disabled, templates will be reloaded on the fly.
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()

	// Recovery middleware handles any go panics generated while processing web
	// requests. Ensures a 500 response is sent to the client rather than
	// sending no response at all.
	router.Use(gin.Recovery())

	router.SetFuncMap(template.FuncMap{
		"incr": func(i int) int { return i + 1 },
		"date": func(t time.Time) string { return t.Format(time.Stamp + " MST") },
	})

	router.Static("/public", "./public/")
	router.LoadHTMLGlob("templates/*")

	router.GET("/", homepage)
	router.GET("/all_nodes", list)
	router.GET("/user_agents", userAgents)
	router.GET("/node", node)

	// API routes
	router.GET("/world_nodes", worldNodes)
	router.GET("/nodes", paginatedNodes)

	return router
}

func Start(ctx context.Context, listen string, mgr *crawler.Manager, requestShutdownChan chan struct{}, shutdownWg *sync.WaitGroup) error {
	amgr = mgr

	// Create TCP listener.
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp", listen)
	if err != nil {
		return err
	}
	log.Printf("Listening on %s", listen)

	srv := http.Server{
		Handler:      NewRouter(),
		ReadTimeout:  5 * time.Second,  // slow requests should not hold connections opened
		WriteTimeout: 60 * time.Second, // hung responses must die
	}

	// Add the graceful shutdown to the waitgroup.
	shutdownWg.Add(1)
	go func() {
		// Wait until shutdown is signaled before shutting down.
		<-ctx.Done()

		// Give the webserver 10 seconds to finish what it is doing.
		timeoutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(timeoutCtx); err != nil {
			log.Printf("Failed to stop webserver cleanly: %v", err)
		} else {
			log.Printf("Webserver stopped")
		}
		shutdownWg.Done()
	}()

	// Start webserver.
	go func() {
		err = srv.Serve(listener)
		// If the server dies for any reason other than ErrServerClosed (from
		// graceful server.Shutdown), log the error and request shutdown.
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("Unexpected webserver error: %v", err)
			requestShutdownChan <- struct{}{}
		}
	}()

	return nil
}
