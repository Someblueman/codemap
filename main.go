package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	codemap "github.com/Someblueman/codemap/internal/codemap"
)

func main() {
	opts := codemap.DefaultOptions()

	flag.StringVar(&opts.ProjectRoot, "root", ".", "Project root directory")
	flag.StringVar(&opts.OutputPath, "output", "CODEMAP.md", "Output file")
	flag.StringVar(&opts.PathsOutputPath, "paths-output", "CODEMAP.paths", "Paths output file")
	flag.IntVar(&opts.LargePackageFiles, "large", 10, "File threshold for detailed listing")
	flag.BoolVar(&opts.IncludeTests, "tests", false, "Include test files")
	flag.BoolVar(&opts.DisablePaths, "no-paths", false, "Disable CODEMAP.paths output")
	flag.BoolVar(&opts.Verbose, "v", false, "Verbose output")
	check := flag.Bool("check", false, "Check staleness only (exit 1 if stale)")
	force := flag.Bool("force", false, "Force regeneration even if outputs are up to date")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if *check {
		stale, err := codemap.IsStale(ctx, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(2)
		}
		if stale {
			fmt.Println("Codemap outputs are stale")
			os.Exit(1)
		}
		fmt.Println("Codemap outputs are up to date")
		os.Exit(0)
	}

	var (
		cm        *codemap.Codemap
		generated bool
		err       error
	)
	if *force {
		cm, err = codemap.Generate(ctx, opts)
		generated = true
	} else {
		cm, generated, err = codemap.EnsureUpToDate(ctx, opts)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if !generated {
		if opts.Verbose {
			fmt.Printf("Codemap outputs are up to date (%s", opts.OutputPath)
			if !opts.DisablePaths {
				fmt.Printf(", %s", opts.PathsOutputPath)
			}
			fmt.Println(")")
		} else {
			fmt.Println("Codemap outputs are up to date")
		}
		return
	}

	if opts.Verbose {
		fmt.Printf("Generated %s", opts.OutputPath)
		if !opts.DisablePaths {
			fmt.Printf(", %s", opts.PathsOutputPath)
		}
		fmt.Printf(": %d packages, %d concerns\n", len(cm.Packages), len(cm.Concerns))
	} else {
		if opts.DisablePaths {
			fmt.Printf("Generated %s\n", opts.OutputPath)
		} else {
			fmt.Printf("Generated %s, %s\n", opts.OutputPath, opts.PathsOutputPath)
		}
	}
}
