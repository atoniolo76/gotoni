/*
Copyright © 2025 ALESSIO TONIOLO

benchmark.go implements benchmarking commands for measuring inference performance.
These commands help measure TTFT (Time To First Token) and prefill rates.
*/
package cmd

import (
	"log"

	"github.com/atoniolo76/gotoni/pkg/benchmark"
	"github.com/spf13/cobra"
)

// benchmarkCmd represents the benchmark command group
var benchmarkCmd = &cobra.Command{
	Use:   "benchmark",
	Short: "Run performance benchmarks and measurements",
	Long: `Benchmarking commands for measuring inference performance metrics.

These commands help you measure Time To First Token (TTFT), prefill rates,
and other performance characteristics of your inference setup.

Examples:
  # Measure TTFT using character-based estimation
  gotoni benchmark measure-char

  # Measure TTFT using local tokenizer (more accurate)
  gotoni benchmark measure-token`,
}

var benchmarkMeasureCharCmd = &cobra.Command{
	Use:   "measure-char",
	Short: "Measure TTFT using character-based estimation",
	Long: `Measure Time To First Token using character-based token estimation.

This benchmark loads prompts from the WildChat dataset and measures TTFT
using character-based token counting (characters/4). It provides a baseline
measurement that doesn't require the CGO tokenizer.`,
	Run: func(cmd *cobra.Command, args []string) {
		log.Println("Starting TTFT measurement with character-based estimation...")
		benchmark.TestMeasureGORGO_MS_PER_CHAR(nil)
		log.Println("✅ Character-based measurement complete!")
	},
}

var benchmarkMeasureTokenCmd = &cobra.Command{
	Use:   "measure-token",
	Short: "Measure TTFT using local CGO tokenizer",
	Long: `Measure Time To First Token using the local CGO tokenizer.

This benchmark provides the most accurate measurements by using the same
tokenizer that the load balancer uses for GORGO routing decisions. It requires
the CGO tokenizer to be properly set up.`,
	Run: func(cmd *cobra.Command, args []string) {
		log.Println("Starting TTFT measurement with CGO tokenizer...")
		// Note: This would need to be implemented in the benchmark package
		// For now, it's a placeholder
		log.Println("⚠️  Token-based measurement not yet implemented in CLI")
		log.Println("   Use the test suite: go test -run TestMeasureGORGO_MS_PER_TOKEN")
	},
}

func init() {
	// Add benchmark command to root
	rootCmd.AddCommand(benchmarkCmd)

	// Add subcommands
	benchmarkCmd.AddCommand(benchmarkMeasureCharCmd)
	benchmarkCmd.AddCommand(benchmarkMeasureTokenCmd)
}