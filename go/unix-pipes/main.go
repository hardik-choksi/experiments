package main

import (
	"log"
	"os"
	"os/exec"
)

type Command struct {
	Name string
	Args []string
	cmd  *exec.Cmd
}

func main() {
	commands := []Command{
		{Name: "cat", Args: []string{"main.go"}},
		{Name: "awk", Args: []string{"/func/"}},
		// {Name: "tr", Args: []string{"a-z", "A-Z"}},
	}

	// Create N-1 pipes (one between each adjacent pair)
	pipes := make([][2]*os.File, len(commands)-1)
	for i := range pipes {
		r, w, err := os.Pipe()
		if err != nil {
			log.Fatalf("pipe: %v", err)
		}
		pipes[i] = [2]*os.File{r, w}
	}

	// Wire up and start all commands
	for i := range commands {
		cmd := exec.Command(commands[i].Name, commands[i].Args...)

		// stdin: first command gets os.Stdin, others get read end of previous pipe
		if i == 0 {
			cmd.Stdin = os.Stdin
		} else {
			cmd.Stdin = pipes[i-1][0]
		}

		// stdout: last command gets os.Stdout, others get write end of next pipe
		if i == len(commands)-1 {
			cmd.Stdout = os.Stdout
		} else {
			cmd.Stdout = pipes[i][1]
		}

		cmd.Stderr = os.Stderr
		commands[i].cmd = cmd

		if err := cmd.Start(); err != nil {
			log.Fatalf("start %s: %v", commands[i].Name, err)
		}
	}

	// Parent closes its copies of all pipe fds — children inherited them already.
	// Without this, readers never get EOF because the parent still holds write ends open.
	for i := range pipes {
		pipes[i][0].Close()
		pipes[i][1].Close()
	}

	// Wait for all commands (like bash does with waitpid for each child)
	for i := range commands {
		if err := commands[i].cmd.Wait(); err != nil {
			log.Printf("%s: %v", commands[i].Name, err)
		}
	}
}
