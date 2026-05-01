package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Usage = printUsage
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		printUsage()
		os.Exit(0)
	}

	switch args[0] {
	case "serve":
		runServe(*configPath)
	case "test":
		if len(args) < 2 {
			fmt.Println("Error: test requires <provider/model> argument")
			fmt.Println("Example: oh-my-api test openai/gpt-4o")
			os.Exit(1)
		}
		runTest(*configPath, args[1])
	case "stats":
		runStats(*configPath, args[1:])
	default:
		fmt.Printf("Unknown command: %s\n", args[0])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("oh-my-api - Personal AI API Gateway")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  oh-my-api [global options] <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  serve    Start API gateway server")
	fmt.Println("  test     Test a provider model connectivity")
	fmt.Println("  stats    Show API usage statistics")
	fmt.Println()
	fmt.Println("Global Options:")
	fmt.Println("  --config string   Path to config file (default: config.yaml)")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  oh-my-api serve")
	fmt.Println("  oh-my-api --config /etc/oh-my-api.yaml serve")
	fmt.Println("  oh-my-api test openai/gpt-4o")
	fmt.Println("  oh-my-api stats")
	fmt.Println("  oh-my-api stats --today")
	fmt.Println("  oh-my-api stats --since \"2026-04-01\" --until \"2026-04-25\"")
	fmt.Println("  oh-my-api stats --reset")
}
