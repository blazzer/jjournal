package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"journal/lj"
	"journal/outbound"
)

func main() {
	fs := flag.NewFlagSet("ljprobe", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	out := fs.String("out", "probe-out", "directory for the probe report")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(1)
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: ljprobe [-out dir]")
		os.Exit(1)
	}
	contact := os.Getenv("OPERATOR_CONTACT")
	if contact == "" {
		contact = "unset"
	}
	client := outbound.New(outbound.Config{Contact: contact}).HTTP(outbound.LaneAPI)
	code, err := lj.RunProbe(context.Background(), lj.ProbeConfig{
		User:     os.Getenv("LJ_USER"),
		Password: os.Getenv("LJ_PASSWORD"),
		OutDir:   *out,
		Client:   client,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ljprobe failed")
		os.Exit(1)
	}
	fmt.Println("wrote", filepath.Join(*out, "report.txt"))
	os.Exit(code)
}
