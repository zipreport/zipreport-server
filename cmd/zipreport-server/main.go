package main

import (
	"flag"
	"fmt"
	"os"
	"zipreport-server/internal"

	"github.com/oddbit-project/blueprint/log"
	"github.com/oddbit-project/blueprint/utils"
)

const ProductName = "ZipReport Server"

// Version is set at build time via -ldflags "-X main.Version=..."
var Version = "3.0.0"

// command-line args
var cliArgs = &internal.CliArgs{
	ConfigFile:   flag.String("c", "config/config.json", "Config file"),
	ShowVersion:  flag.Bool("version", false, "Show version"),
	SampleConfig: flag.Bool("sample-config", false, "Show available config file options and default values"),
}

func main() {
	// config logger
	utils.PanicOnError(log.Configure(log.NewDefaultConfig()))
	logger := log.New("zipreport-server")

	flag.Parse()

	if *cliArgs.ShowVersion {
		fmt.Printf("Version: %s\n", Version)
		os.Exit(0)
	}

	if *cliArgs.SampleConfig {
		cfg := internal.NewConfig()
		result, _ := cfg.DumpDefaults()
		fmt.Println(result)
		os.Exit(0)
	}

	app, err := internal.NewZipReport(cliArgs, logger)
	if err != nil {
		logger.Error(err, "Initialization failed")
		os.Exit(-1)
	}

	app.Build(ProductName)
	app.Start()
}
