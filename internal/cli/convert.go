package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/levmv/polka/internal/converter"
)

func runConvert(ctx context.Context, _ string, args []string) error {
	fs := commandFlagSet("convert", "polka convert --to <format> [--force] <source> <output>")
	target := fs.String("to", "", "target format (currently: "+supportedConvertTargets()+")")
	force := fs.Bool("force", false, "overwrite the output file if it already exists")
	if help, err := parseCommandFlags(fs, args); help || err != nil {
		return err
	}
	if fs.NArg() != 2 || *target == "" {
		fs.Usage()
		return reportedErrorf("usage: polka convert --to <format> [--force] <source> <output>")
	}

	srcPath := fs.Arg(0)
	dstPath := fs.Arg(1)
	opts := converter.ConversionOptions{OnWarning: func(message string) {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", message)
	}}
	if err := converter.ConvertFileWithOptions(ctx, srcPath, dstPath, converter.NormalizeTarget(*target), *force, opts); err != nil {
		return err
	}
	fmt.Printf("Converted: %s\n", dstPath)
	return nil
}

func supportedConvertTargets() string {
	specs := converter.SupportedTargetSpecs()
	targets := make([]string, 0, len(specs))
	for _, spec := range specs {
		targets = append(targets, string(spec.Target))
	}
	return strings.Join(targets, ", ")
}
