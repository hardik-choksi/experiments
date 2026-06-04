package main

type TrieNode struct {
	endOfWord bool // if any word has ended on this node
	children  [26]*TrieNode
}

func Insert(root *TrieNode, word string) {
	curr := root
	for _, ch := range word {
		idx := ch - 'a'
		if curr.children[idx] == nil {
			curr.children[idx] = &TrieNode{}
		}
		curr = curr.children[idx]
	}
	curr.endOfWord = true
}

// NewTrie builds a trie from given words and return pointer to the root node of the tree
func NewTrie(words []string) *TrieNode {
	root := &TrieNode{}

	for _, word := range words {
		Insert(root, word)
	}

	return root
}

func dfs(node *TrieNode, res *[]string, str *[]rune) {
	if node == nil {
		return
	}

	if node.endOfWord {
		*res = append(*res, string(*str))
	}

	for j, v := range node.children {
		if v != nil {
			*str = append(*str, rune(j+'a'))
			dfs(v, res, str)
			*str = (*str)[:len(*str)-1]
		}
	}
}

func StartsWith(root *TrieNode, word string) []string {
	curr := root

	for _, ch := range word {
		if curr.children[ch-'a'] == nil {
			return []string{}
		}
		curr = curr.children[ch-'a']
	}

	var res []string

	str := []rune(word)
	dfs(curr, &res, &str)

	return res
}

func GetAllWords(root *TrieNode) []string {
	var res []string

	var str []rune
	dfs(root, &res, &str)

	return res
}

func Search(root *TrieNode, word string) bool {
	curr := root

	for _, ch := range word {
		if curr.children[ch-'a'] == nil {
			return false
		}
		curr = curr.children[ch-'a']
	}

	return curr.endOfWord
}
