package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/chzyer/readline"
	"github.com/eiannone/keyboard"
	"github.com/k0kubun/pp"
)

var completer = readline.NewPrefixCompleter(
	readline.PcItem("help"),
	readline.PcItem("exit"),
	readline.PcItem("config",
		readline.PcItem("set",
			readline.PcItem("timeout"),
			readline.PcItem("retries"),
		),
		readline.PcItem("get"),
	),
)

func main() {
	// tr := NewTrie(words)
	// words := []string{"eat", "eatery", "pant", "panty"}
	rd := bufio.NewReader(os.Stdin)
	fmt.Printf("Add list of words separated by (,):\n")
	fmt.Print("> ")
	ip, err := rd.ReadString('\n')
	ip = strings.TrimRight(ip, "\n")
	if err != nil {
		fmt.Println(err)
		os.Exit(-1)
	}
	root := &TrieNode{}
	for str := range strings.SplitSeq(ip, ",") {
		Insert(root, strings.TrimSpace(str))
	}
	fmt.Println("----------------------------------------------")
	fmt.Println("Trie is ready")

	for {
		fmt.Println("----------------------------------------------")
		fmt.Println("1: Search")
		fmt.Println("2: StartsWith")
		fmt.Println("3: GetAllWords")
		fmt.Println("4: Check Tab functions")
		fmt.Println("----------------------------------------------")
		fmt.Println("Please enter in {option number}:{word} manner")
		fmt.Println("----------------------------------------------")
		fmt.Print("> ")
		nStr, err := rd.ReadString('\n')
		nStr = strings.TrimRight(nStr, "\n")
		if err != nil {
			fmt.Println("---------ERROR------------------------------")
			fmt.Println(err)
			fmt.Println("--------END-ERROR---------------------------")
			continue
		}
		parts := strings.Split(nStr, ":")
		n, err := strconv.Atoi(parts[0])
		if err != nil {
			fmt.Println("---------ERROR------------------------------")
			fmt.Println(err)
			fmt.Println("--------END-ERROR---------------------------")
			continue
		}
		switch n {
		case 1:
			pp.Println(Search(root, parts[1]))
		case 2:
			pp.Println(StartsWith(root, parts[1]))
		case 3:
			pp.Println(GetAllWords(root))
		case 4:
			allWords := GetAllWords(root)
			pp.Println("--------------------")
			pp.Println("Available Words:", allWords)
			pp.Println("--------------------")
			pp.Println("Type prefix and press Tab for suggestions, Enter to submit, Esc to cancel")
			pp.Println("--------------------")

			if err := keyboard.Open(); err != nil {
				pp.Println(err)
				continue
			}

			var typed []rune
			fmt.Print("$ ")
		keyloop:
			for {
				char, key, err := keyboard.GetKey()
				if err != nil {
					pp.Println(err)
					break
				}
				switch key {
				case keyboard.KeyCtrlC:
					keyboard.Close()
					fmt.Print("\r\n")
					os.Exit(0)
				case keyboard.KeyTab:
					prefix := string(typed)
					suggestions := StartsWith(root, prefix)
					fmt.Printf("\r\n  suggestions for %q: %v\r\n", prefix, suggestions)
					fmt.Printf("$ %s", prefix)
				case keyboard.KeyEnter:
					fmt.Printf("\r\nYou typed: %q\r\n", string(typed))
					break keyloop
				case keyboard.KeyEsc:
					fmt.Print("\r\n(cancelled)\r\n")
					break keyloop
				case keyboard.KeyBackspace, keyboard.KeyBackspace2:
					if len(typed) > 0 {
						typed = typed[:len(typed)-1]
						fmt.Print("\b \b")
					}
				case keyboard.KeySpace:
					typed = append(typed, ' ')
					fmt.Print(" ")
				default:
					if char != 0 {
						typed = append(typed, char)
						fmt.Print(string(char))
					}
				}
			}
			keyboard.Close()
		default:
			pp.Println("Invalid Option")
		}
	}
	// rl, err := readline.NewEx(&readline.Config{
	// 	Prompt:       "$ ",
	// 	AutoComplete: completer,
	// })
	// if err != nil {
	// 	log.Fatal(err)
	// }
	// defer rl.Close()

	// for {
	// 	line, err := rl.Readline()
	// 	if err != nil { // io.EOF on Ctrl+D
	// 		break
	// 	}
	// 	// handle line
	// 	pp.Println(line)
	// }
}
