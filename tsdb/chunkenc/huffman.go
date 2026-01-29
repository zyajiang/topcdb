package chunkenc

import (
	"container/heap"
	"sort"
)

type TreeNode struct {
	val   int
	times int
	left  *TreeNode
	right *TreeNode
}

type MinHeap []*TreeNode

func (h MinHeap) Less(i, j int) bool {
	if h[i].times == h[j].times {
		return h[i].val > h[j].val
	}
	return h[i].times < h[j].times
}

func (h MinHeap) Len() int {
	return len(h)
}

func (h *MinHeap) Swap(i, j int) {
	(*h)[i], (*h)[j] = (*h)[j], (*h)[i]
}

func (h *MinHeap) Push(node interface{}) {
	*h = append(*h, node.(*TreeNode))
}

func (h *MinHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

type HuffmanDecoder struct {
	root *TreeNode
	br   bstreamReader

	val int64
}

func NewHuffmanDecoder(b []byte, ptr1 uint32) *HuffmanDecoder {
	m, ptr2 := deserializeHuffmanTree(b, int(ptr1))

	nodelist := make(MinHeap, 0)
	for k, v := range m {
		nodelist = append(nodelist, &TreeNode{val: k, times: v})
	}
	sort.Sort(&nodelist)
	heap.Init(&nodelist)
	if nodelist.Len() == 0 {
		return nil
	}

	Tree := initHuffmanTree(nodelist)
	return &HuffmanDecoder{
		root: Tree,
		br:   newBReader(b[ptr2:]),

		val: 0,
	}
}

func (hd *HuffmanDecoder) Next() bool {
	node := hd.root
	for node.left != nil || node.right != nil {
		bit, err := hd.br.readBit()
		if err != nil {
			return false
		}
		if bit {
			node = node.right
		} else {
			node = node.left
		}
	}
	hd.val += int64(node.val)
	return true
}

func (hd *HuffmanDecoder) Read() int64 {
	return hd.val
}

func HuffmanEncodeWithoutTimesMap(input *[]int, ptr int) *bstream {
	m := make(map[int]int)
	for _, v := range *input {
		cnt, ok := m[v]
		if ok {
			m[v] = cnt + 1
		} else {
			m[v] = 1
		}
	}

	nodelist := make(MinHeap, 0)
	for k, v := range m {
		nodelist = append(nodelist, &TreeNode{val: k, times: v})
	}
	sort.Sort(&nodelist)
	heap.Init(&nodelist)
	if nodelist.Len() == 0 {
		return nil
	}

	Tree := initHuffmanTree(nodelist)
	encodeTab := make(map[int]string)
	createEncodingTable(Tree, encodeTab)
	b := serializeHuffmanTree(m, ptr)
	if b != nil {
		huffmanEncoding(input, encodeTab, b)
	}
	return b
}

func deserializeHuffmanTree(b []byte, ptr int) (map[int]int, int) {
	m := make(map[int]int)

	len1, len2 := readHuffmanMeta(b, ptr)
	valueDecoder := NewSimple8bDecoder(b[ptr+8 : ptr+8+int(len1)])
	timesDecoder := NewSimple8bDecoder(b[ptr+8+int(len1) : ptr+8+int(len1)+int(len2)])

	t, v := 0, 0
	for valueDecoder.Next() && timesDecoder.Next() {
		if t == 0 {
			value := int(valueDecoder.Read())
			if value%2 == 0 {
				value = value / 2
			} else {
				value = -(value + 1) / 2
			}
			v = value
		} else {
			value := int(valueDecoder.Read())
			v += value
		}
		times := int(timesDecoder.Read())
		if times%2 == 0 {
			times = times / 2
		} else {
			times = -(times + 1) / 2
		}
		t += times
		m[v] = t
	}

	return m, ptr + 8 + int(len1) + int(len2)
}

func serializeHuffmanTree(timesTab map[int]int, ptr int) *bstream {
	valueEncoder, timesEncoder := NewSimple8bEncoder(), NewSimple8bEncoder()
	keys := make([]int, 0)
	for key := range timesTab {
		keys = append(keys, key)
	}
	sort.Ints(keys)
	val, times := keys[0], 0

	if keys[0] < 0 {
		valueEncoder.Write(uint64(-2*keys[0] - 1))
	} else {
		valueEncoder.Write(uint64(2 * keys[0]))
	}
	for _, key := range keys[1:] {
		valueEncoder.Write(uint64(key - val))
		val = key
	}

	for _, key := range keys {
		temp := timesTab[key] - times
		if temp >= 0 {
			timesEncoder.Write(uint64(temp * 2))
		} else {
			timesEncoder.Write(uint64(-temp*2 - 1))
		}
		times = timesTab[key]
	}

	b1, err := valueEncoder.Bytes()
	if err != nil {
		return nil
	}
	b2, err := timesEncoder.Bytes()
	if err != nil {
		return nil
	}

	buf := make([]byte, ptr+8+len(b1)+len(b2))
	b := &bstream{stream: buf, count: 0}
	writeHuffmanMeta(uint32(len(b1)), uint32(len(b2)), b, ptr)
	copy(b.stream[ptr+8:ptr+8+len(b1)], b1)
	copy(b.stream[ptr+8+len(b1):ptr+8+len(b1)+len(b2)], b2)

	return b
}

func initHuffmanTree(nodelist MinHeap) *TreeNode {
	for nodelist.Len() > 1 {
		lnode := heap.Pop(&nodelist).(*TreeNode)
		rnode := heap.Pop(&nodelist).(*TreeNode)
		root := &TreeNode{times: lnode.times + rnode.times}
		if lnode.times > rnode.times {
			root.left, root.right = rnode, lnode
		} else {
			root.left, root.right = lnode, rnode
		}
		heap.Push(&nodelist, root)
	}
	return nodelist[0]
}

func createEncodingTable(node *TreeNode, encodeTab map[int]string) {
	tmp := make([]byte, 0)
	var dfs func(treeNode *TreeNode)
	dfs = func(root *TreeNode) {
		if root == nil {
			return
		}
		if root.left == nil && root.right == nil {
			encodeTab[root.val] = string(tmp)
		}
		tmp = append(tmp, '0')
		dfs(root.left)
		tmp[len(tmp)-1] = '1'
		dfs(root.right)
		tmp = tmp[:len(tmp)-1]
	}
	dfs(node)
}

func huffmanEncoding(input *[]int, encodeTab map[int]string, b *bstream) {
	for _, v := range *input {
		for _, c := range encodeTab[v] {
			if c == '1' {
				b.writeBit(true)
			} else {
				b.writeBit(false)
			}
		}
	}
}

func writeHuffmanMeta(ptr1 uint32, ptr2 uint32, b *bstream, ptr int) {
	for i := 0; i < 4; i += 1 {
		b.stream[ptr+i] = byte(ptr1 >> (24 - 8*i))
	}
	for i := 0; i < 4; i += 1 {
		b.stream[ptr+4+i] = byte(ptr2 >> (24 - 8*i))
	}
}

func readHuffmanMeta(b []byte, ptr int) (uint32, uint32) {
	ptr1 := uint32(b[ptr])<<24 + uint32(b[ptr+1])<<16 + uint32(b[ptr+2])<<8 + uint32(b[ptr+3])
	ptr2 := uint32(b[ptr+4])<<24 + uint32(b[ptr+5])<<16 + uint32(b[ptr+6])<<8 + uint32(b[ptr+7])
	return ptr1, ptr2
}
