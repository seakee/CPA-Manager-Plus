package runtimeconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type options struct {
	InputPath  string
	OutputPath string
}

func Run(args []string, stdout io.Writer, stderr io.Writer) error {
	opts, err := parseArgs(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := sanitize(opts.InputPath, opts.OutputPath); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "Sanitized native runtime config written to %s.\n", opts.OutputPath)
	return nil
}

func parseArgs(args []string, stderr io.Writer) (options, error) {
	var opts options
	fs := flag.NewFlagSet("sanitize-runtime-config", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.InputPath, "input", "", "existing runtime config path")
	fs.StringVar(&opts.OutputPath, "output", "", "sanitized runtime config path")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: cpa-manager-plus sanitize-runtime-config --input PATH --output PATH")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if fs.NArg() > 0 {
		return options{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	opts.InputPath = strings.TrimSpace(opts.InputPath)
	opts.OutputPath = strings.TrimSpace(opts.OutputPath)
	if opts.InputPath == "" || opts.OutputPath == "" {
		return options{}, errors.New("--input and --output are required")
	}
	return opts, nil
}

func sanitize(inputPath string, outputPath string) error {
	info, err := os.Lstat(inputPath)
	if err != nil {
		return fmt.Errorf("inspect runtime config %s: %w", inputPath, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("runtime config is not a regular file: %s", inputPath)
	}
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return fmt.Errorf("read runtime config %s: %w", inputPath, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil {
		return fmt.Errorf("decode runtime config %s: %w", inputPath, err)
	}
	if fields == nil {
		return fmt.Errorf("runtime config %s must contain a JSON object", inputPath)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("runtime config %s contains multiple JSON values", inputPath)
		}
		return fmt.Errorf("decode trailing runtime config data %s: %w", inputPath, err)
	}
	delete(fields, "cpaUpstreamUrl")
	delete(fields, "managementKeyFile")
	output, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return fmt.Errorf("encode sanitized runtime config: %w", err)
	}
	output = append(output, '\n')
	if err := writeAtomic(outputPath, output, info.Mode().Perm()); err != nil {
		return fmt.Errorf("write sanitized runtime config %s: %w", outputPath, err)
	}
	return nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) (returnErr error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(directory, ".cpamp-runtime-config-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() {
		if returnErr != nil {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	return nil
}
