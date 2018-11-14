package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jirenius/resgate/logger"
	"github.com/jirenius/resgate/nats"
	"github.com/jirenius/resgate/server"
)

var stopTimeout = 10 * time.Second

var usageStr = `
Usage: resgate [options]

Server Options:
    -n, --nats <url>                 NATS Server URL (default: nats://127.0.0.1:4222)
    -p, --port <port>                HTTP port for client connections (default: 8080)
    -w, --wspath <path>              WebSocket path for clients (default: /)
    -a, --apipath <path>             Web resource path for clients (default: /api/)
    -r, --reqtimeout <seconds>       Timeout duration for NATS requests (default: 5)
    -u, --headauth <method>          Resource method for header authentication
        --tls                        Enable TLS for HTTP (default: false)
        --tlscert <file>             HTTP server certificate file
        --tlskey <file>              Private key for HTTP server certificate
    -c, --config <file>              Configuration file

Common Options:
    -h, --help                       Show this message
`

// Config holds server configuration
type Config struct {
	NatsURL        string `json:"natsUrl"`
	RequestTimeout int    `json:"requestTimeout"`
	Debug          bool   `json:"debug,omitempty"`
	server.Config
}

// SetDefault sets the default values
func (c *Config) SetDefault() {
	if c.NatsURL == "" {
		c.NatsURL = "nats://127.0.0.1:4222"
	}
	if c.RequestTimeout == 0 {
		c.RequestTimeout = 5
	}
	c.Config.SetDefault()
}

// Init takes a path to a json encoded file and loads the config
// If no file exists, a new file with default settings is created
func (c *Config) Init(fs *flag.FlagSet, args []string) error {
	var (
		showHelp   bool
		configFile string
		port       uint
		headauth   string
	)

	fs.BoolVar(&showHelp, "h", false, "Show this message.")
	fs.BoolVar(&showHelp, "help", false, "Show this message.")
	fs.StringVar(&configFile, "c", "", "Configuration file.")
	fs.StringVar(&configFile, "config", "", "Configuration file.")
	fs.StringVar(&c.NatsURL, "n", "", "NATS Server URL.")
	fs.StringVar(&c.NatsURL, "nats", "", "NATS Server URL.")
	fs.UintVar(&port, "p", 0, "HTTP port for client connections.")
	fs.UintVar(&port, "port", 0, "HTTP port for client connections.")
	fs.StringVar(&c.WSPath, "w", "", "WebSocket path for clients.")
	fs.StringVar(&c.WSPath, "wspath", "", "WebSocket path for clients.")
	fs.StringVar(&c.APIPath, "a", "", "Web resource path for clients.")
	fs.StringVar(&c.APIPath, "apipath", "", "Web resource path for clients.")
	fs.StringVar(&headauth, "u", "", "Resource method for header authentication.")
	fs.StringVar(&headauth, "headauth", "", "Resource method for header authentication.")
	fs.BoolVar(&c.TLS, "tls", false, "Enable TLS for HTTP.")
	fs.StringVar(&c.TLSCert, "tlscert", "", "HTTP server certificate file.")
	fs.StringVar(&c.TLSKey, "tlskey", "", "Private key for HTTP server certificate.")
	fs.IntVar(&c.RequestTimeout, "r", 0, "Timeout duration for NATS requests.")
	fs.IntVar(&c.RequestTimeout, "reqtimeout", 0, "Timeout duration for NATS requests.")

	if err := fs.Parse(args); err != nil {
		printAndDie(fmt.Sprintf("error parsing arguments: %s", err.Error()), true)
	}

	if port >= 1<<16 {
		printAndDie(fmt.Sprintf(`invalid port "%d": must be less than 65536`, port), true)
	}

	if showHelp {
		usage()
	}

	if configFile != "" {
		fin, err := ioutil.ReadFile(configFile)
		if err != nil {
			if !os.IsNotExist(err) {
				return fmt.Errorf("error loading config file: %s", err)
			}

			c.SetDefault()

			fout, err := json.MarshalIndent(c, "", "\t")
			if err != nil {
				return fmt.Errorf("error encoding config: %s", err)
			}

			ioutil.WriteFile(configFile, fout, os.FileMode(0664))
		} else {
			err = json.Unmarshal(fin, c)
			if err != nil {
				return fmt.Errorf("error parsing config file: %s", err)
			}

			// Overwrite configFile options with command line options
			fs.Parse(args)
		}
	}

	if port > 0 {
		c.Port = uint16(port)
	}

	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "u":
			fallthrough
		case "headauth":
			if headauth == "" {
				c.HeaderAuth = nil
			} else {
				c.HeaderAuth = &headauth
			}
		}
	})

	// Any value not set, set it now
	c.SetDefault()

	return nil
}

// usage will print out the flag options for the server.
func usage() {
	fmt.Printf("%s\n", usageStr)
	os.Exit(0)
}

func printAndDie(msg string, showUsage bool) {
	fmt.Fprintf(os.Stderr, "%s\n", msg)
	if showUsage {
		fmt.Fprintf(os.Stderr, "%s\n", usageStr)
	}
	os.Exit(1)
}

func main() {
	fs := flag.NewFlagSet("resgate", flag.ExitOnError)
	fs.Usage = usage

	var cfg Config

	err := cfg.Init(fs, os.Args[1:])
	if err != nil {
		printAndDie(err.Error(), false)
	}

	l := logger.NewStdLogger(cfg.Debug, cfg.Debug)
	serv := server.NewService(&nats.Client{
		URL:            cfg.NatsURL,
		RequestTimeout: time.Duration(cfg.RequestTimeout) * time.Second,
		Logger:         l,
	}, cfg.Config)
	serv.SetLogger(l)

	if err := serv.Start(); err != nil {
		printAndDie(err.Error(), false)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop,
		os.Interrupt,
		syscall.SIGHUP,
		syscall.SIGTERM,
		syscall.SIGQUIT)

	select {
	case <-stop:
	case err := <-serv.StopChannel():
		if err != nil {
			printAndDie(err.Error(), false)
		}
	}
	// Await for waitGroup to be done
	done := make(chan struct{})
	go func() {
		defer close(done)
		serv.Stop(nil)
	}()

	select {
	case <-done:
	case <-time.After(stopTimeout):
		panic("Shutdown timed out")
	}
}
