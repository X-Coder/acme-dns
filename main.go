//+build !test

package main

import (
	"crypto/tls"
	stdlog "log"
	"net/http"
	"os"
	"syscall"

	"github.com/julienschmidt/httprouter"
	"github.com/rs/cors"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/acme/autocert"
)

func main() {
	// Created files are not world writable
	syscall.Umask(0077)
	// Read global config
	var err error
	if fileIsAccessible("/etc/acme-dns/config.cfg") {
		log.WithFields(log.Fields{"file": "/etc/acme-dns/config.cfg"}).Info("Using config file")
		Config, err = readConfig("/etc/acme-dns/config.cfg")
	} else if fileIsAccessible("./config.cfg") {
		log.WithFields(log.Fields{"file": "./config.cfg"}).Info("Using config file")
		Config, err = readConfig("./config.cfg")
	} else {
		log.Errorf("Configuration file not found.")
		os.Exit(1)
	}
	if err != nil {
		log.Errorf("Encountered an error while trying to read configuration file:  %s", err)
		os.Exit(1)
	}

	setupLogging(Config.Logconfig.Format, Config.Logconfig.Level)

	// Read the default records in
	RR.Parse(Config.General)

	// Open database
	newDB := new(acmedb)
	err = newDB.Init(Config.Database.Engine, Config.Database.Connection)
	if err != nil {
		log.Errorf("Could not open database [%v]", err)
		os.Exit(1)
	} else {
		log.Info("Connected to database")
	}
	DB = newDB
	defer DB.Close()

	// DNS server
	startDNS(Config.General.Listen, Config.General.Proto)

	// HTTP API
	startHTTPAPI()

	log.Debugf("Shutting down...")
}

func startHTTPAPI() {
	// Setup http logger
	logger := log.New()
	logwriter := logger.Writer()
	defer logwriter.Close()
	api := httprouter.New()
	c := cors.New(cors.Options{
		AllowedOrigins:     Config.API.CorsOrigins,
		AllowedMethods:     []string{"GET", "POST"},
		OptionsPassthrough: false,
		Debug:              Config.General.Debug,
	})
	if Config.General.Debug {
		// Logwriter for saner log output
		c.Log = stdlog.New(logwriter, "", 0)
	}
	if !Config.API.DisableRegistration {
		api.POST("/register", webRegisterPost)
	}
	api.POST("/update", Auth(webUpdatePost))

	host := Config.API.IP + ":" + Config.API.Port

	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	switch Config.API.TLS {
	case "letsencrypt":
		m := autocert.Manager{
			Cache:      autocert.DirCache(Config.API.ACMECacheDir),
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(Config.API.Domain),
		}
		autocerthost := Config.API.IP + ":" + Config.API.AutocertPort
		log.WithFields(log.Fields{"autocerthost": autocerthost, "domain": Config.API.Domain}).Debug("Opening HTTP port for autocert")
		go http.ListenAndServe(autocerthost, m.HTTPHandler(nil))
		cfg.GetCertificate = m.GetCertificate
		srv := &http.Server{
			Addr:      host,
			Handler:   c.Handler(api),
			TLSConfig: cfg,
			ErrorLog:  stdlog.New(logwriter, "", 0),
		}
		log.WithFields(log.Fields{"host": host, "domain": Config.API.Domain}).Info("Listening HTTPS, using certificate from autocert")
		log.Fatal(srv.ListenAndServeTLS("", ""))
	case "cert":
		srv := &http.Server{
			Addr:      host,
			Handler:   c.Handler(api),
			TLSConfig: cfg,
			ErrorLog:  stdlog.New(logwriter, "", 0),
		}
		log.WithFields(log.Fields{"host": host}).Info("Listening HTTPS")
		log.Fatal(srv.ListenAndServeTLS(Config.API.TLSCertFullchain, Config.API.TLSCertPrivkey))
	default:
		log.WithFields(log.Fields{"host": host}).Info("Listening HTTP")
		log.Fatal(http.ListenAndServe(host, c.Handler(api)))
	}
}
