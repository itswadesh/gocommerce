package gocommerce

import (
	"fmt"
	"strings"
)

// A QR code encoder, drawn on the server so a printed code is the same from
// every screen and the panel needs no library (D255). It covers what a store
// prints: byte mode, error correction level M (15% of the symbol can be
// smudged or torn), versions 1 to 10, which hold up to 213 bytes — a link,
// not a document.
//
// The construction is ISO/IEC 18004's, in the order of Nayuki's reference
// implementation: function patterns, data and error correction codewords
// interleaved by block, the zigzag placement, and the mask with the lowest
// penalty.

// qrMaxVersion is the largest symbol drawn. Version 10 is 57 modules a side.
const qrMaxVersion = 10

// Level M's error correction codewords per block, and its number of blocks,
// for versions 1–10 (ISO/IEC 18004 table 9). Index 0 is unused.
var (
	qrECCPerBlock = [qrMaxVersion + 1]int{0, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26}
	qrBlocks      = [qrMaxVersion + 1]int{0, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5}
)

// QRCodeSVG draws text as a QR code: black modules on white with the four
// module quiet zone a scanner needs, one path, sized by its viewBox so it
// prints sharp at any size.
func QRCodeSVG(text string) (string, error) {
	m, err := qrEncode([]byte(text), -1)
	if err != nil {
		return "", err
	}
	const quiet = 4
	side := len(m) + 2*quiet
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges">`, side, side)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, side, side)
	for y, row := range m {
		for x := 0; x < len(row); {
			if !row[x] {
				x++
				continue
			}
			start := x
			for x < len(row) && row[x] {
				x++
			}
			fmt.Fprintf(&b, "M%d %dh%dv1h-%dz", start+quiet, y+quiet, x-start, x-start)
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String(), nil
}

// qrEncode returns the symbol's modules, true for dark, row by row. mask is
// 0–7 to force one, which only a test does; -1 picks the lowest penalty.
func qrEncode(data []byte, mask int) ([][]bool, error) {
	version := 0
	for v := 1; v <= qrMaxVersion; v++ {
		if 4+qrCountBits(v)+8*len(data) <= 8*qrDataCodewords(v) {
			version = v
			break
		}
	}
	if version == 0 {
		return nil, Validationf("that is too long for a QR code here; keep it under %d characters", qrCapacity())
	}

	q := newQRSymbol(version)
	q.drawCodewords(qrInterleave(version, qrDataBytes(version, data)))
	if mask < 0 {
		best := -1
		for m := 0; m < 8; m++ {
			q.applyMask(m)
			q.drawFormat(m)
			if p := q.penalty(); best < 0 || p < best {
				best, mask = p, m
			}
			q.applyMask(m) // the mask is its own inverse
		}
	}
	q.applyMask(mask)
	q.drawFormat(mask)
	return q.modules, nil
}

// qrDataBytes is the data codewords in byte mode: the mode indicator, the
// length, the bytes, a terminator of up to four zero bits, then padding to the
// version's capacity.
func qrDataBytes(version int, data []byte) []byte {
	capBits := 8 * qrDataCodewords(version)
	var bits qrBits
	bits.put(0b0100, 4)
	bits.put(len(data), qrCountBits(version))
	for _, c := range data {
		bits.put(int(c), 8)
	}
	bits.put(0, min(4, capBits-len(bits)))
	bits.put(0, (8-len(bits)%8)%8)
	for pad := 0xEC; len(bits) < capBits; pad ^= 0xEC ^ 0x11 {
		bits.put(pad, 8)
	}
	out := make([]byte, len(bits)/8)
	for i, bit := range bits {
		if bit {
			out[i>>3] |= 1 << (7 - i&7)
		}
	}
	return out
}

// qrCapacity is the most bytes the largest symbol holds.
func qrCapacity() int {
	return (8*qrDataCodewords(qrMaxVersion) - 4 - qrCountBits(qrMaxVersion)) / 8
}

func qrCountBits(version int) int {
	if version < 10 {
		return 8
	}
	return 16
}

// qrRawModules is how many modules of a version carry data or error
// correction: the whole symbol less every function pattern and format area.
func qrRawModules(version int) int {
	n := (16*version+128)*version + 64
	if version >= 2 {
		align := version/7 + 2
		n -= (25*align-10)*align - 55
		if version >= 7 {
			n -= 36
		}
	}
	return n
}

func qrDataCodewords(version int) int {
	return qrRawModules(version)/8 - qrECCPerBlock[version]*qrBlocks[version]
}

// qrInterleave splits the data into the version's blocks, appends each block's
// Reed-Solomon codewords, and reads the blocks out column by column, as a
// scanner expects. Where blocks differ in length the short ones come first.
func qrInterleave(version int, data []byte) []byte {
	blocks, eccLen := qrBlocks[version], qrECCPerBlock[version]
	raw := qrRawModules(version) / 8
	short, shortLen := blocks-raw%blocks, raw/blocks
	divisor := rsDivisor(eccLen)
	var all [][]byte
	k := 0
	for i := 0; i < blocks; i++ {
		n := shortLen - eccLen
		if i >= short {
			n++
		}
		block := append([]byte{}, data[k:k+n]...)
		k += n
		ecc := rsRemainder(block, divisor)
		if i < short {
			block = append(block, 0) // a placeholder, so every block is as long
		}
		all = append(all, append(block, ecc...))
	}
	var out []byte
	for i := range all[0] {
		for j, block := range all {
			if i != shortLen-eccLen || j >= short {
				out = append(out, block[i])
			}
		}
	}
	return out
}

// rsDivisor is the Reed-Solomon generator polynomial of a degree, without its
// leading 1, over GF(2^8) with the QR code's polynomial 0x11D.
func rsDivisor(degree int) []byte {
	out := make([]byte, degree)
	out[degree-1] = 1
	root := byte(1)
	for i := 0; i < degree; i++ {
		for j := range out {
			out[j] = gfMul(out[j], root)
			if j+1 < len(out) {
				out[j] ^= out[j+1]
			}
		}
		root = gfMul(root, 2)
	}
	return out
}

func rsRemainder(data, divisor []byte) []byte {
	out := make([]byte, len(divisor))
	for _, b := range data {
		factor := b ^ out[0]
		copy(out, out[1:])
		out[len(out)-1] = 0
		for i, d := range divisor {
			out[i] ^= gfMul(d, factor)
		}
	}
	return out
}

func gfMul(x, y byte) byte {
	z := 0
	for i := 7; i >= 0; i-- {
		z = (z << 1) ^ ((z >> 7) * 0x11D)
		z ^= int(y>>i&1) * int(x)
	}
	return byte(z)
}

type qrBits []bool

func (b *qrBits) put(value, n int) {
	for i := n - 1; i >= 0; i-- {
		*b = append(*b, value>>i&1 == 1)
	}
}

// qrSymbol is the grid being drawn: the modules, and which of them are
// function patterns, which data and masking leave alone.
type qrSymbol struct {
	size     int
	modules  [][]bool
	function [][]bool
}

func newQRSymbol(version int) *qrSymbol {
	size := version*4 + 17
	q := &qrSymbol{size: size, modules: make([][]bool, size), function: make([][]bool, size)}
	for i := range q.modules {
		q.modules[i] = make([]bool, size)
		q.function[i] = make([]bool, size)
	}
	for i := 0; i < size; i++ {
		q.set(6, i, i%2 == 0)
		q.set(i, 6, i%2 == 0)
	}
	q.finder(3, 3)
	q.finder(size-4, 3)
	q.finder(3, size-4)
	pos := qrAlignment(version)
	for i, x := range pos {
		for j, y := range pos {
			last := len(pos) - 1
			if (i == 0 && j == 0) || (i == 0 && j == last) || (i == last && j == 0) {
				continue // the finder patterns are there
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					q.set(x+dx, y+dy, max(abs(dx), abs(dy)) != 1)
				}
			}
		}
	}
	q.drawFormat(0) // reserves the format area; the real bits come with the mask
	if version >= 7 {
		rem := version
		for i := 0; i < 12; i++ {
			rem = (rem << 1) ^ ((rem >> 11) * 0x1F25)
		}
		bits := version<<12 | rem
		for i := 0; i < 18; i++ {
			dark := bits>>i&1 == 1
			a, b := size-11+i%3, i/3
			q.set(a, b, dark)
			q.set(b, a, dark)
		}
	}
	return q
}

// set draws a function module at column x, row y.
func (q *qrSymbol) set(x, y int, dark bool) {
	q.modules[y][x] = dark
	q.function[y][x] = true
}

func (q *qrSymbol) finder(cx, cy int) {
	for dy := -4; dy <= 4; dy++ {
		for dx := -4; dx <= 4; dx++ {
			x, y := cx+dx, cy+dy
			if x >= 0 && x < q.size && y >= 0 && y < q.size {
				d := max(abs(dx), abs(dy))
				q.set(x, y, d != 2 && d != 4)
			}
		}
	}
}

// qrAlignment is the row and column centres of a version's alignment patterns.
func qrAlignment(version int) []int {
	if version == 1 {
		return nil
	}
	n := version/7 + 2
	step := (version*8 + n*3 + 5) / (n*4 - 4) * 2
	out := make([]int, n)
	out[0] = 6
	for i, p := n-1, version*4+10; i >= 1; i, p = i-1, p-step {
		out[i] = p
	}
	return out
}

// drawFormat writes level M and the mask, BCH-protected and XOR-masked, in
// both copies, with the dark module beside the lower-left finder.
func (q *qrSymbol) drawFormat(mask int) {
	data := 0b00<<3 | mask // level M is 00
	rem := data
	for i := 0; i < 10; i++ {
		rem = (rem << 1) ^ ((rem >> 9) * 0x537)
	}
	bits := (data<<10 | rem) ^ 0x5412
	bit := func(i int) bool { return bits>>i&1 == 1 }
	for i := 0; i <= 5; i++ {
		q.set(8, i, bit(i))
	}
	q.set(8, 7, bit(6))
	q.set(8, 8, bit(7))
	q.set(7, 8, bit(8))
	for i := 9; i < 15; i++ {
		q.set(14-i, 8, bit(i))
	}
	for i := 0; i < 8; i++ {
		q.set(q.size-1-i, 8, bit(i))
	}
	for i := 8; i < 15; i++ {
		q.set(8, q.size-15+i, bit(i))
	}
	q.set(8, q.size-8, true)
}

// drawCodewords lays the bits in two-column strips from the bottom right,
// up and down in turn, skipping the vertical timing column.
func (q *qrSymbol) drawCodewords(data []byte) {
	i := 0
	for right := q.size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := 0; vert < q.size; vert++ {
			for j := 0; j < 2; j++ {
				x, y := right-j, vert
				if (right+1)&2 == 0 {
					y = q.size - 1 - vert
				}
				if !q.function[y][x] && i < len(data)*8 {
					q.modules[y][x] = data[i>>3]>>(7-i&7)&1 == 1
					i++
				}
			}
		}
	}
}

func (q *qrSymbol) applyMask(mask int) {
	for y := 0; y < q.size; y++ {
		for x := 0; x < q.size; x++ {
			var flip bool
			switch mask {
			case 0:
				flip = (x+y)%2 == 0
			case 1:
				flip = y%2 == 0
			case 2:
				flip = x%3 == 0
			case 3:
				flip = (x+y)%3 == 0
			case 4:
				flip = (x/3+y/2)%2 == 0
			case 5:
				flip = x*y%2+x*y%3 == 0
			case 6:
				flip = (x*y%2+x*y%3)%2 == 0
			case 7:
				flip = ((x+y)%2+x*y%3)%2 == 0
			}
			if flip && !q.function[y][x] {
				q.modules[y][x] = !q.modules[y][x]
			}
		}
	}
}

// penalty scores a masked symbol by the standard's four rules: long runs of
// one colour, 2×2 blocks, patterns a scanner could mistake for a finder, and
// a dark share far from half. Lower scans more reliably.
func (q *qrSymbol) penalty() int {
	n, score, dark := q.size, 0, 0
	at := func(x, y int, transpose bool) bool {
		if transpose {
			return q.modules[x][y]
		}
		return q.modules[y][x]
	}
	finderLike := [2][11]bool{
		{true, false, true, true, true, false, true, false, false, false, false},
		{false, false, false, false, true, false, true, true, true, false, true},
	}
	for _, t := range []bool{false, true} {
		for y := 0; y < n; y++ {
			run := 1
			for x := 1; x <= n; x++ {
				if x < n && at(x, y, t) == at(x-1, y, t) {
					run++
					continue
				}
				if run >= 5 {
					score += run - 2
				}
				run = 1
			}
			for x := 0; x+11 <= n; x++ {
				for _, p := range finderLike {
					match := true
					for k := 0; k < 11 && match; k++ {
						match = at(x+k, y, t) == p[k]
					}
					if match {
						score += 40
					}
				}
			}
		}
	}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			c := q.modules[y][x]
			if c {
				dark++
			}
			if x+1 < n && y+1 < n && c == q.modules[y][x+1] && c == q.modules[y+1][x] && c == q.modules[y+1][x+1] {
				score += 3
			}
		}
	}
	total := n * n
	k := (abs(dark*20-total*10)+total-1)/total - 1
	return score + k*10
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
