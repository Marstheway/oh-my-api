package token

import (
	_ "embed"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

//go:embed deepseek_v3_tokenizer.json
var embeddedTokenizer []byte

// tokenizerAsset 对应 deepseek_v3_tokenizer.json 顶层结构。
type tokenizerAsset struct {
	Model struct {
		Vocab  map[string]int `json:"vocab"`
		Merges []string       `json:"merges"`
	} `json:"model"`
	AddedTokens []addedToken `json:"added_tokens"`
}

type addedToken struct {
	ID      int    `json:"id"`
	Content string `json:"content"`
	Special bool   `json:"special"`
}

// deepseekTokenizer 是 DeepSeek V3 BPE 的高性能自实现。
// 所有查表结构在加载阶段一次性构建，CountTokens 主循环无额外分配。
type deepseekTokenizer struct {
	vocab         map[string]int // token 字符串 -> ID
	idToToken     []string       // ID -> token 字符串
	mergeRank     map[[2]int]int // (idA, idB) -> 合并优先级（越小越高）
	byteToChar    [256]rune      // 字节值 -> ByteLevel Unicode 码点
	specialSorted []string       // 所有 added token 按长度降序，用于前缀匹配
}

// loadDeepseekTokenizer 从嵌入的 JSON 资产文件加载并初始化 deepseek tokenizer。
func loadDeepseekTokenizer() error {
	if len(embeddedTokenizer) == 0 {
		return errors.New("embedded tokenizer data is empty")
	}

	var asset tokenizerAsset
	if err := json.Unmarshal(embeddedTokenizer, &asset); err != nil {
		return err
	}

	dt := &deepseekTokenizer{}

	// 1. 构建 ByteLevel 字节→码点映射（HuggingFace 标准方式）
	dt.buildByteToChar()

	// 2. 构建 vocab 和 idToToken
	maxID := 0
	for _, id := range asset.Model.Vocab {
		if id > maxID {
			maxID = id
		}
	}
	dt.vocab = asset.Model.Vocab
	dt.idToToken = make([]string, maxID+1)
	for token, id := range dt.vocab {
		dt.idToToken[id] = token
	}

	// 3. 构建 merge rank 表
	dt.mergeRank = make(map[[2]int]int, len(asset.Model.Merges))
	for rank, merge := range asset.Model.Merges {
		parts := strings.SplitN(merge, " ", 2)
		if len(parts) != 2 {
			continue
		}
		idA, okA := dt.vocab[parts[0]]
		idB, okB := dt.vocab[parts[1]]
		if !okA || !okB {
			continue
		}
		dt.mergeRank[[2]int{idA, idB}] = rank
	}

	// 4. 构建整体匹配列表：所有 added_tokens 都加入，不区分 special 字段。
	// special=false 的条目（如 <think>、<｜User｜>、<｜tool*｜> 等）同样不在 model.vocab
	// 里，必须在切分前整体匹配，否则会被逐字节拆碎导致 token 计数明显高估。
	for _, at := range asset.AddedTokens {
		dt.specialSorted = append(dt.specialSorted, at.Content)
	}
	// 按长度降序排列，确保优先匹配最长 token
	sort.Slice(dt.specialSorted, func(i, j int) bool {
		return len(dt.specialSorted[i]) > len(dt.specialSorted[j])
	})

	deepseekTk = dt
	return nil
}

// buildByteToChar 构建 HuggingFace ByteLevel 标准的字节到 Unicode 码点映射。
// 等价于 tokenizers 库中 bytes_to_unicode() 的输出。
func (d *deepseekTokenizer) buildByteToChar() {
	// 直接映射自身的字节集合：
	// 33-126: ASCII 可打印字符
	// 161-172, 174-255: Latin-1 补充字符（排除 U+00AD 软连字符）
	// 其余字节（0-32, 127-160, 173）按顺序映射到 256+n
	n := 0
	for b := 0; b < 256; b++ {
		if isDirectByte(b) {
			d.byteToChar[b] = rune(b)
		} else {
			d.byteToChar[b] = rune(256 + n)
			n++
		}
	}
}

// isDirectByte 判断字节是否直接映射为自身的 Unicode 码点。
func isDirectByte(b int) bool {
	return (b >= 33 && b <= 126) ||
		(b >= 161 && b <= 172) ||
		(b >= 174 && b <= 255)
}

// CountTokens 使用 DeepSeek V3 BPE 规则对 text 进行 token 计数。
// 主循环直接在原始 string 上按字节位置推进，避免 []rune 转换和 matchSpecial 的重复拷贝。
func (d *deepseekTokenizer) CountTokens(text string) int {
	if text == "" {
		return 0
	}

	pos := 0
	total := 0

	for pos < len(text) {
		// 优先整体匹配 added token（含 special=true 和 special=false 条目）
		if n := d.matchSpecial(text, pos); n > 0 {
			total++ // added token 整体计为 1
			pos += n
			continue
		}

		// 规则 1：1~3 个 Unicode 数字（对应资产 \p{N}{1,3}）
		if n := d.matchDigits(text, pos); n > 0 {
			total += d.bpeCount(text[pos : pos+n])
			pos += n
			continue
		}

		// 规则 2：最长连续 CJK 块
		if n := d.matchCJK(text, pos); n > 0 {
			total += d.bpeCount(text[pos : pos+n])
			pos += n
			continue
		}

		// 规则 3a：标点 + ASCII 字母
		if n := d.matchPunctLetters(text, pos); n > 0 {
			total += d.bpeCount(text[pos : pos+n])
			pos += n
			continue
		}

		// 规则 3b：可选前导 + 字母/音标
		if n := d.matchLettersMarks(text, pos); n > 0 {
			total += d.bpeCount(text[pos : pos+n])
			pos += n
			continue
		}

		// 规则 3c：可选前导空格 + 标点/符号 + 可选换行
		if n := d.matchPunctSymbol(text, pos); n > 0 {
			total += d.bpeCount(text[pos : pos+n])
			pos += n
			continue
		}

		// 规则 3d：可选前导空白 + 换行
		if n := d.matchNewlines(text, pos); n > 0 {
			total += d.bpeCount(text[pos : pos+n])
			pos += n
			continue
		}

		// 规则 3e：仅出现在文本末尾的空白串（\s+(?!\S)）
		if n := d.matchTrailingWhitespace(text, pos); n > 0 {
			total += d.bpeCount(text[pos : pos+n])
			pos += n
			continue
		}

		// 规则 3f：其他空白串
		if n := d.matchWhitespace(text, pos); n > 0 {
			total += d.bpeCount(text[pos : pos+n])
			pos += n
			continue
		}

		// 回退：单个 rune 作为独立块，保证不会卡死
		_, w := utf8.DecodeRuneInString(text[pos:])
		total += d.bpeCount(text[pos : pos+w])
		pos += w
	}

	return total
}

// bpeCount 对已切分的文本块执行 ByteLevel BPE 并返回 token 数。
// 内部按字节→码点→BPE 合并的流程处理，不会 panic。
func (d *deepseekTokenizer) bpeCount(chunk string) int {
	if chunk == "" {
		return 0
	}

	bytes := []byte(chunk)
	tokenIDs := make([]int, 0, len(bytes))

	for _, b := range bytes {
		r := d.byteToChar[b]
		id, ok := d.vocab[string(r)]
		if !ok {
			// 单字节映射失败，保守回退（至少计 1 个 token）
			n := len(bytes) / 4
			if n == 0 {
				n = 1
			}
			return n
		}
		tokenIDs = append(tokenIDs, id)
	}

	if len(tokenIDs) == 1 {
		return 1
	}

	// BPE 合并循环：每一轮找优先级最高（rank 最小）的可合并对
	maxRank := len(d.mergeRank) + 1
	for {
		bestRank := maxRank
		bestIdx := -1

		for i := 0; i < len(tokenIDs)-1; i++ {
			pair := [2]int{tokenIDs[i], tokenIDs[i+1]}
			rank, ok := d.mergeRank[pair]
			if ok && rank < bestRank {
				bestRank = rank
				bestIdx = i
			}
		}

		if bestIdx == -1 {
			break
		}

		// 合并 tokenIDs[bestIdx] 和 tokenIDs[bestIdx+1]
		merged := d.idToToken[tokenIDs[bestIdx]] + d.idToToken[tokenIDs[bestIdx+1]]
		newID, ok := d.vocab[merged]
		if !ok {
			// 合并后不在 vocab（不应发生），保守返回当前 token 数
			return len(tokenIDs)
		}

		// 原地替换：先删掉 bestIdx+1，再把 bestIdx 赋为新 ID
		tokenIDs = append(tokenIDs[:bestIdx], tokenIDs[bestIdx+1:]...)
		tokenIDs[bestIdx] = newID
	}

	return len(tokenIDs)
}

// matchSpecial 检查 text[pos:] 是否以某个 added token 开头。
// 返回消耗的字节数；未命中返回 0。
func (d *deepseekTokenizer) matchSpecial(text string, pos int) int {
	rem := text[pos:]
	for _, special := range d.specialSorted {
		if strings.HasPrefix(rem, special) {
			return len(special)
		}
	}
	return 0
}

// matchDigits 匹配 1~3 个 Unicode 数字（对应资产 \p{N}{1,3}）。
// 返回消耗的字节数。
func (d *deepseekTokenizer) matchDigits(text string, pos int) int {
	if pos >= len(text) {
		return 0
	}
	bpos := pos
	count := 0
	for count < 3 && bpos < len(text) {
		r, w := utf8.DecodeRuneInString(text[bpos:])
		if !unicode.IsNumber(r) {
			break
		}
		bpos += w
		count++
	}
	return bpos - pos
}

// matchCJK 匹配最长连续 CJK 字符。返回消耗的字节数。
func (d *deepseekTokenizer) matchCJK(text string, pos int) int {
	if pos >= len(text) {
		return 0
	}
	r, w := utf8.DecodeRuneInString(text[pos:])
	if !isCJK(r) {
		return 0
	}
	bpos := pos + w
	for bpos < len(text) {
		r, w = utf8.DecodeRuneInString(text[bpos:])
		if !isCJK(r) {
			break
		}
		bpos += w
	}
	return bpos - pos
}

// matchPunctLetters 匹配"标点+ASCII字母"模式。返回消耗的字节数。
func (d *deepseekTokenizer) matchPunctLetters(text string, pos int) int {
	if pos >= len(text) {
		return 0
	}
	r0, w0 := utf8.DecodeRuneInString(text[pos:])
	if !isRulePunct(r0) {
		return 0
	}
	bpos := pos + w0
	if bpos >= len(text) {
		return 0
	}
	r1, w1 := utf8.DecodeRuneInString(text[bpos:])
	if !isASCIILetter(r1) {
		return 0
	}
	bpos += w1
	for bpos < len(text) {
		r, w := utf8.DecodeRuneInString(text[bpos:])
		if !isASCIILetter(r) {
			break
		}
		bpos += w
	}
	return bpos - pos
}

// matchLettersMarks 匹配"可选非换行非字母/标点/符号前导 + 字母/音标"模式。
// 返回消耗的字节数。
func (d *deepseekTokenizer) matchLettersMarks(text string, pos int) int {
	if pos >= len(text) {
		return 0
	}
	bpos := pos

	// 可选前导：[^\r\n\p{L}\p{P}\p{S}]
	r0, w0 := utf8.DecodeRuneInString(text[bpos:])
	if !isCRLF(r0) && !unicode.IsLetter(r0) && !unicode.IsPunct(r0) && !unicode.IsSymbol(r0) {
		bpos += w0
		if bpos >= len(text) {
			return 0
		}
	}

	// 必须：[\p{L}\p{M}]+
	r, w := utf8.DecodeRuneInString(text[bpos:])
	if !unicode.IsLetter(r) && !unicode.IsMark(r) {
		return 0
	}
	bpos += w
	for bpos < len(text) {
		r, w = utf8.DecodeRuneInString(text[bpos:])
		if !unicode.IsLetter(r) && !unicode.IsMark(r) {
			break
		}
		bpos += w
	}
	return bpos - pos
}

// matchPunctSymbol 匹配"可选前导空格 + 标点/符号串 + 可选换行"模式。
// 返回消耗的字节数。
func (d *deepseekTokenizer) matchPunctSymbol(text string, pos int) int {
	if pos >= len(text) {
		return 0
	}
	bpos := pos

	// 可选前导空格
	r0, w0 := utf8.DecodeRuneInString(text[bpos:])
	if r0 == ' ' {
		bpos += w0
		if bpos >= len(text) {
			return 0
		}
	}

	// 必须：[\p{P}\p{S}]+
	r, w := utf8.DecodeRuneInString(text[bpos:])
	if !unicode.IsPunct(r) && !unicode.IsSymbol(r) {
		return 0
	}
	bpos += w
	for bpos < len(text) {
		r, w = utf8.DecodeRuneInString(text[bpos:])
		if !unicode.IsPunct(r) && !unicode.IsSymbol(r) {
			break
		}
		bpos += w
	}

	// 可选：[\r\n]+
	for bpos < len(text) {
		r, w = utf8.DecodeRuneInString(text[bpos:])
		if !isCRLF(r) {
			break
		}
		bpos += w
	}
	return bpos - pos
}

// matchNewlines 匹配"可选前导空白 + 换行"模式。返回消耗的字节数。
func (d *deepseekTokenizer) matchNewlines(text string, pos int) int {
	if pos >= len(text) {
		return 0
	}
	bpos := pos

	// 可选前导空白（非 CR/LF 的空白字符）
	for bpos < len(text) {
		r, w := utf8.DecodeRuneInString(text[bpos:])
		if !unicode.IsSpace(r) || isCRLF(r) {
			break
		}
		bpos += w
	}

	// 必须：[\r\n]+
	if bpos >= len(text) {
		return 0
	}
	r, w := utf8.DecodeRuneInString(text[bpos:])
	if !isCRLF(r) {
		return 0
	}
	bpos += w
	for bpos < len(text) {
		r, w = utf8.DecodeRuneInString(text[bpos:])
		if !isCRLF(r) {
			break
		}
		bpos += w
	}
	return bpos - pos
}

// matchTrailingWhitespace 匹配"仅出现在文本末尾的空白串"（模拟 \s+(?!\S)）。
// 通过顺序扫描判断当前空白后面是否还有非空白字符。返回消耗的字节数。
func (d *deepseekTokenizer) matchTrailingWhitespace(text string, pos int) int {
	if pos >= len(text) {
		return 0
	}
	r, _ := utf8.DecodeRuneInString(text[pos:])
	if !unicode.IsSpace(r) {
		return 0
	}
	// 检查从 pos 到末尾是否全为空白
	bpos := pos
	for bpos < len(text) {
		r, w := utf8.DecodeRuneInString(text[bpos:])
		if !unicode.IsSpace(r) {
			return 0 // 后面还有非空白字符，不算末尾空白
		}
		bpos += w
	}
	return len(text) - pos
}

// matchWhitespace 匹配通用空白串。返回消耗的字节数。
func (d *deepseekTokenizer) matchWhitespace(text string, pos int) int {
	if pos >= len(text) {
		return 0
	}
	r, w := utf8.DecodeRuneInString(text[pos:])
	if !unicode.IsSpace(r) {
		return 0
	}
	bpos := pos + w
	for bpos < len(text) {
		r, w = utf8.DecodeRuneInString(text[bpos:])
		if !unicode.IsSpace(r) {
			break
		}
		bpos += w
	}
	return bpos - pos
}

// ---- helper functions ----

func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FA5) ||
		(r >= 0x3040 && r <= 0x309F) ||
		(r >= 0x30A0 && r <= 0x30FF)
}

func isASCIILetter(r rune) bool {
	return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
}

func isRulePunct(r rune) bool {
	// 对应资产中的标点集合：[!"#$%&'()*+,\-./:;<=>?@\[\\\]^_`{|}~]
	switch r {
	case '!', '"', '#', '$', '%', '&', '\'', '(', ')', '*', '+',
		',', '-', '.', '/', ':', ';', '<', '=', '>', '?', '@',
		'[', '\\', ']', '^', '_', '`', '{', '|', '}', '~':
		return true
	}
	return false
}

func isCRLF(r rune) bool {
	return r == '\r' || r == '\n'
}
