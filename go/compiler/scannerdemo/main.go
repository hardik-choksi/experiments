package main

import (
	"fmt"
	"go/scanner"
	"go/token"
)

func main() {
	src := []byte(`package main

import "fmt"

func main() {
    fmt.Println("Hello world")
}`)

	var s scanner.Scanner
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	s.Init(file, src, nil, scanner.ScanComments)

	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		fmt.Printf("%s\t%s\t%q\n", fset.Position(pos), tok, lit)
	}
}
