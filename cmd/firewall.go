/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/atoniolo76/gotoni/pkg/remote"
	"github.com/spf13/cobra"
)

// firewallCmd represents the firewall command
var firewallCmd = &cobra.Command{
	Use:   "firewall",
	Short: "Manage firewall rules",
	Long:  `Manage inbound firewall rules for your Lambda Cloud instances.`,
}

// firewallListCmd lists current firewall rules
var firewallListCmd = &cobra.Command{
	Use:   "list",
	Short: "List current firewall rules",
	Long:  `List all inbound firewall rules currently active for your account.`,
	Run: func(cmd *cobra.Command, args []string) {
		apiToken, err := cmd.Flags().GetString("api-token")
		if err != nil {
			log.Fatalf("Error getting API token: %v", err)
		}

		if apiToken == "" {
			apiToken = remote.GetAPIToken()
			if apiToken == "" {
				log.Fatal("API token not provided via --api-token flag or LAMBDA_API_KEY environment variable")
			}
		}

		httpClient := remote.NewHTTPClient()
		ruleset, err := remote.GetGlobalFirewallRules(httpClient, apiToken)
		if err != nil {
			log.Fatalf("Error listing firewall rules: %v", err)
		}

		if len(ruleset.Rules) == 0 {
			fmt.Println("No firewall rules configured.")
			return
		}

		fmt.Println("Current firewall rules:")
		fmt.Println()
		for i, rule := range ruleset.Rules {
			fmt.Printf("Rule %d:\n", i+1)
			fmt.Printf("  Protocol:       %s\n", rule.Protocol)
			if len(rule.PortRange) == 2 {
				fmt.Printf("  Port Range:     %d-%d\n", rule.PortRange[0], rule.PortRange[1])
			}
			fmt.Printf("  Source Network: %s\n", rule.SourceNetwork)
			fmt.Printf("  Description:    %s\n", rule.Description)
			fmt.Println()
		}
	},
}

// firewallReplaceCmd replaces all firewall rules
var firewallReplaceCmd = &cobra.Command{
	Use:   "replace",
	Short: "Replace all firewall rules",
	Long: `Replace all inbound firewall rules with the provided rules.

This command accepts rules in one of two formats:

1. JSON format (--json flag):
   gotoni firewall replace --json '[{"protocol":"tcp","port_range":[22,22],"source_network":"0.0.0.0/0","description":"SSH"}]'

2. Shorthand format (--rule flag, can be repeated):
   gotoni firewall replace --rule "tcp:22:0.0.0.0/0:SSH" --rule "tcp:80-443:0.0.0.0/0:HTTP/HTTPS"

   Shorthand format: protocol:port_or_range:source_network:description
   - port_or_range can be a single port (22) or a range (80-443)
   - For ICMP, omit port: "icmp::0.0.0.0/0:Ping"

Note: Firewall rules do not apply to the us-south-1 region.`,
	Run: func(cmd *cobra.Command, args []string) {
		apiToken, err := cmd.Flags().GetString("api-token")
		if err != nil {
			log.Fatalf("Error getting API token: %v", err)
		}

		if apiToken == "" {
			apiToken = remote.GetAPIToken()
			if apiToken == "" {
				log.Fatal("API token not provided via --api-token flag or LAMBDA_API_KEY environment variable")
			}
		}

		jsonRules, _ := cmd.Flags().GetString("json")
		shorthandRules, _ := cmd.Flags().GetStringArray("rule")

		var rules []remote.FirewallRule

		if jsonRules != "" {
			// Parse JSON format
			err = json.Unmarshal([]byte(jsonRules), &rules)
			if err != nil {
				log.Fatalf("Error parsing JSON rules: %v", err)
			}
		} else if len(shorthandRules) > 0 {
			// Parse shorthand format
			for _, r := range shorthandRules {
				rule, err := parseShorthandRule(r)
				if err != nil {
					log.Fatalf("Error parsing rule '%s': %v", r, err)
				}
				rules = append(rules, rule)
			}
		} else {
			log.Fatal("Either --json or --rule must be provided")
		}

		httpClient := remote.NewHTTPClient()
		resultRuleset, err := remote.UpdateGlobalFirewallRules(httpClient, apiToken, rules)
		if err != nil {
			log.Fatalf("Error replacing firewall rules: %v", err)
		}

		fmt.Println("Firewall rules replaced successfully!")
		fmt.Printf("Applied %d rule(s):\n\n", len(resultRuleset.Rules))
		for i, rule := range resultRuleset.Rules {
			fmt.Printf("Rule %d:\n", i+1)
			fmt.Printf("  Protocol:       %s\n", rule.Protocol)
			if len(rule.PortRange) == 2 {
				fmt.Printf("  Port Range:     %d-%d\n", rule.PortRange[0], rule.PortRange[1])
			}
			fmt.Printf("  Source Network: %s\n", rule.SourceNetwork)
			fmt.Printf("  Description:    %s\n", rule.Description)
			fmt.Println()
		}
	},
}

// parseShorthandRule parses a rule in format "protocol:port_or_range:source_network:description"
func parseShorthandRule(s string) (remote.FirewallRule, error) {
	parts := strings.SplitN(s, ":", 4)
	if len(parts) < 4 {
		return remote.FirewallRule{}, fmt.Errorf("invalid format, expected protocol:port_or_range:source_network:description")
	}

	protocol := strings.ToLower(parts[0])
	portStr := parts[1]
	sourceNetwork := parts[2]
	description := parts[3]

	// Validate protocol
	validProtocols := map[string]bool{"tcp": true, "udp": true, "icmp": true, "all": true}
	if !validProtocols[protocol] {
		return remote.FirewallRule{}, fmt.Errorf("invalid protocol '%s', must be one of: tcp, udp, icmp, all", protocol)
	}

	rule := remote.FirewallRule{
		Protocol:      protocol,
		SourceNetwork: sourceNetwork,
		Description:   description,
	}

	// Parse port range (not required for icmp)
	if portStr != "" {
		if strings.Contains(portStr, "-") {
			// Range format: 80-443
			rangeParts := strings.Split(portStr, "-")
			if len(rangeParts) != 2 {
				return remote.FirewallRule{}, fmt.Errorf("invalid port range format '%s'", portStr)
			}
			minPort, err := strconv.Atoi(rangeParts[0])
			if err != nil {
				return remote.FirewallRule{}, fmt.Errorf("invalid min port '%s': %v", rangeParts[0], err)
			}
			maxPort, err := strconv.Atoi(rangeParts[1])
			if err != nil {
				return remote.FirewallRule{}, fmt.Errorf("invalid max port '%s': %v", rangeParts[1], err)
			}
			rule.PortRange = []int{minPort, maxPort}
		} else {
			// Single port: 22
			port, err := strconv.Atoi(portStr)
			if err != nil {
				return remote.FirewallRule{}, fmt.Errorf("invalid port '%s': %v", portStr, err)
			}
			rule.PortRange = []int{port, port}
		}
	} else if protocol != "icmp" {
		return remote.FirewallRule{}, fmt.Errorf("port_range is required for protocol '%s'", protocol)
	}

	return rule, nil
}

func init() {
	rootCmd.AddCommand(firewallCmd)
	firewallCmd.AddCommand(firewallListCmd)
	firewallCmd.AddCommand(firewallReplaceCmd)

	// Flags for list command
	firewallListCmd.Flags().StringP("api-token", "a", "", "API token for cloud provider (can also be set via LAMBDA_API_KEY env var)")

	// Flags for replace command
	firewallReplaceCmd.Flags().StringP("api-token", "a", "", "API token for cloud provider (can also be set via LAMBDA_API_KEY env var)")
	firewallReplaceCmd.Flags().StringP("json", "j", "", "JSON array of firewall rules")
	firewallReplaceCmd.Flags().StringArrayP("rule", "r", []string{}, "Firewall rule in shorthand format: protocol:port_or_range:source_network:description")
}
