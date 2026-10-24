package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/0226zy/proto-platform/agent-runtime/internal/domain/entity"
	"github.com/0226zy/proto-platform/agent-runtime/internal/domain/port"
)

// skipDirs 是 glob/grep 默认跳过的目录（除非模式显式点名）。
var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true}

// ── glob ─────────────────────────────────────────────────────────

// GlobTool 按模式定位文件（指引 §2.3.1 #6：不读内容，只返回路径列表）。
type GlobTool struct {
	maxResults int
	maxBytes   int
}

// NewGlobTool 的可选 searchMax 限制累计返回路径字节数（默认 1 MiB）。
func NewGlobTool(maxResults int, searchMax ...int) *GlobTool {
	maxBytes := 1 << 20
	if len(searchMax) > 0 && searchMax[0] > 0 {
		maxBytes = searchMax[0]
	}
	return &GlobTool{maxResults: maxResults, maxBytes: maxBytes}
}

func (t *GlobTool) Definition() entity.ToolDefinition {
	return entity.ToolDefinition{
		Type: "function",
		Function: entity.FunctionSpec{
			Name: "glob",
			Description: `按路径模式定位文件，只返回路径列表，不读取内容。支持 ** 跨层匹配（如 "**/*.go"、"cmd/**"、"internal/*/README.md"）。
何时使用：按文件名/路径找文件，了解目录结构。
何时不要用：按文件内容搜索用 grep；已知确切路径直接 read_file。
参数：pattern 必填；path 可选，指定搜索根目录（默认当前工作目录）。
失败形态：根目录不存在返回 IsError=true；结果超过上限时截断并注明总数。`,
			Parameters: schema(map[string]any{
				"pattern": strProp("路径模式，例如 \"**/*.go\" 或 \"docs/**/*.md\""),
				"path":    strProp("可选，搜索根目录（默认当前工作目录）"),
			}, "pattern"),
		},
	}
}

func (t *GlobTool) ConcurrencySafe() bool { return true }

type globArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

func (t *GlobTool) Execute(ctx context.Context, argsJSON string, tctx *port.ToolContext) entity.ToolResult {
	var args globArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf("参数解析失败（应为 JSON 对象，含必填字段 pattern: string）：%v", err)}
	}
	if args.Pattern == "" {
		return entity.ToolResult{IsError: true, Content: "参数 pattern 不能为空"}
	}
	root := tctx.Cwd()
	if args.Path != "" {
		root = absPath(tctx.Cwd(), args.Path)
	}
	if _, err := os.Stat(root); err != nil {
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf("根目录不可访问: %v", err)}
	}

	patSegs := strings.Split(filepath.ToSlash(args.Pattern), "/")
	var matches []string
	total := 0
	matchesBytes := 0
	searchLimited := false
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if p != root && skipDirs[info.Name()] && !strings.Contains(args.Pattern, info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return nil
		}
		if globMatch(patSegs, strings.Split(filepath.ToSlash(rel), "/")) {
			total++
			if len(matches) >= t.maxResults {
				return nil
			}
			entryBytes := len(p) + 1
			if matchesBytes+entryBytes > t.maxBytes {
				searchLimited = true
				return nil
			}
			matches = append(matches, p)
			matchesBytes += entryBytes
		}
		return nil
	})
	if total == 0 {
		return entity.ToolResult{Content: fmt.Sprintf("没有文件匹配模式 %q（根目录 %s）", args.Pattern, root)}
	}
	sort.Strings(matches)
	var b strings.Builder
	for _, m := range matches {
		b.WriteString(m)
		b.WriteByte('\n')
	}
	if searchLimited {
		fmt.Fprintf(&b, "…… [共 %d 个匹配，仅显示 %d 个；已达到 search_max_bytes 累计路径字节上限 %d，请缩小模式范围] ……\n", total, len(matches), t.maxBytes)
	} else if total > len(matches) {
		fmt.Fprintf(&b, "…… [共 %d 个匹配，仅显示前 %d 个，请缩小模式范围] ……\n", total, len(matches))
	}
	return entity.ToolResult{Content: b.String()}
}

// globMatch 逐段匹配路径模式，"**" 匹配任意数量（含零）的路径段。
func globMatch(patSegs, pathSegs []string) bool {
	if len(patSegs) == 0 {
		return len(pathSegs) == 0
	}
	if patSegs[0] == "**" {
		// ** 匹配零段或多段
		if globMatch(patSegs[1:], pathSegs) {
			return true
		}
		return len(pathSegs) > 0 && globMatch(patSegs, pathSegs[1:])
	}
	if len(pathSegs) == 0 {
		return false
	}
	ok, err := filepath.Match(patSegs[0], pathSegs[0])
	if err != nil || !ok {
		return false
	}
	return globMatch(patSegs[1:], pathSegs[1:])
}

// ── grep ─────────────────────────────────────────────────────────

// GrepTool 按内容搜索（指引 §2.3.1 #7：返回「文件:行号:内容」）。
type GrepTool struct {
	maxMatches int
	maxBytes   int
}

// NewGrepTool 的可选 searchMax 限制单次搜索的累计文件读取字节数（默认 1 MiB）。
func NewGrepTool(maxMatches int, searchMax ...int) *GrepTool {
	maxBytes := 1 << 20
	if len(searchMax) > 0 && searchMax[0] > 0 {
		maxBytes = searchMax[0]
	}
	return &GrepTool{maxMatches: maxMatches, maxBytes: maxBytes}
}

func (t *GrepTool) Definition() entity.ToolDefinition {
	return entity.ToolDefinition{
		Type: "function",
		Function: entity.FunctionSpec{
			Name: "grep",
			Description: `按正则表达式搜索文件内容，返回 "文件路径:行号:内容" 格式的匹配行。
何时使用：找符号定义、引用点、关键词出现位置。
何时不要用：按文件名找文件用 glob；已知位置读内容用 read_file。
参数：pattern 必填（Go RE2 正则）；path 可选，搜索根（默认当前工作目录）；include 可选，按文件名过滤（如 "*.go"）；case_insensitive 可选。
默认跳过 .git / node_modules / vendor、二进制与超过 5MB 的文件。
失败形态：正则非法返回 IsError=true 与错误说明；匹配或累计读取超上限时截断并注明。`,
			Parameters: schema(map[string]any{
				"pattern":          strProp("正则表达式，例如 \"func \\w+Handler\" 或 \"TODO|FIXME\""),
				"path":             strProp("可选，搜索根目录或单个文件（默认当前工作目录）"),
				"include":          strProp("可选，文件名过滤模式，如 \"*.go\""),
				"case_insensitive": boolProp("可选，true 时忽略大小写，默认 false"),
			}, "pattern"),
		},
	}
}

func (t *GrepTool) ConcurrencySafe() bool { return true }

type grepArgs struct {
	Pattern         string `json:"pattern"`
	Path            string `json:"path"`
	Include         string `json:"include"`
	CaseInsensitive bool   `json:"case_insensitive"`
}

func (t *GrepTool) Execute(ctx context.Context, argsJSON string, tctx *port.ToolContext) entity.ToolResult {
	var args grepArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf("参数解析失败（应为 JSON 对象，含必填字段 pattern: string）：%v", err)}
	}
	if args.Pattern == "" {
		return entity.ToolResult{IsError: true, Content: "参数 pattern 不能为空"}
	}
	pattern := args.Pattern
	if args.CaseInsensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf("正则表达式非法（RE2 语法）: %v", err)}
	}

	root := tctx.Cwd()
	if args.Path != "" {
		root = absPath(tctx.Cwd(), args.Path)
	}
	info, err := os.Stat(root)
	if err != nil {
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf("搜索路径不可访问: %v", err)}
	}

	var files []string
	if !info.IsDir() {
		if info.Size() <= 5*1024*1024 {
			files = []string{root}
		}
	} else {
		_ = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if fi.IsDir() {
				if p != root && skipDirs[fi.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if fi.Size() > 5*1024*1024 {
				return nil
			}
			if args.Include != "" {
				if ok, _ := filepath.Match(args.Include, fi.Name()); !ok {
					return nil
				}
			}
			files = append(files, p)
			return nil
		})
	}

	var b strings.Builder
	total := 0
	shown := 0
	truncated := false
	searchLimited := false
	var readBytes int64
outer:
	for _, f := range files {
		remaining := int64(t.maxBytes) - readBytes
		if remaining <= 0 {
			searchLimited = true
			break
		}
		raw, tooLarge, readErr := readFileBounded(f, remaining)
		if tooLarge {
			searchLimited = true
			break
		}
		if readErr != nil {
			continue
		}
		readBytes += int64(len(raw))
		if isBinary(raw) {
			continue
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if re.MatchString(line) {
				total++
				if shown < t.maxMatches {
					shown++
					if len(line) > 300 {
						line = line[:300] + "…"
					}
					fmt.Fprintf(&b, "%s:%d:%s\n", f, i+1, line)
				} else {
					truncated = true
					break outer
				}
			}
		}
	}
	if total == 0 && !searchLimited {
		return entity.ToolResult{Content: fmt.Sprintf("没有匹配 %q 的内容", args.Pattern)}
	}
	if truncated {
		fmt.Fprintf(&b, "…… [已达到显示上限 %d 处，匹配可能更多，请缩小范围（path/include/更精确的正则）] ……\n", shown)
	}
	if searchLimited {
		fmt.Fprintf(&b, "…… [已达到 search_max_bytes 累计读取上限 %d 字节；搜索未覆盖全部候选文件，请缩小范围] ……\n", t.maxBytes)
	}
	return entity.ToolResult{Content: b.String()}
}
