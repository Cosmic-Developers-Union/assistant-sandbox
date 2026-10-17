package main

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type journalReport struct {
	Suite, Version, Target string
	RunComplete            bool
	CampaignCoverage       string
	Counts                 map[string]int
	Results                []journalResult
}
type journalResult struct {
	Case               string
	Description        string
	Activity, TP, Code int
	Result             string
}

func parseJournal(in io.Reader) (journalReport, error) {
	r := journalReport{CampaignCoverage: "未知：需套件实际测试计划与全部必选项", Counts: map[string]int{}}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	seen := map[string]bool{}
	cases := map[int]string{}
	started := false
	line := 0
	names := []string{"PASS", "FAIL", "UNRESOLVED", "NOTINUSE", "UNSUPPORTED", "UNTESTED", "UNINITIATED", "NORESULT"}
	for scanner.Scan() {
		line++
		row := scanner.Text()
		if strings.TrimSpace(row) == "" {
			continue
		}
		fields := strings.SplitN(row, "|", 3)
		if len(fields) != 3 {
			return r, fmt.Errorf("journal 第%d行格式无效", line)
		}
		kind, err := strconv.Atoi(fields[0])
		if err != nil {
			return r, fmt.Errorf("journal 第%d行记录码: %w", line, err)
		}
		if r.RunComplete {
			return r, fmt.Errorf("journal 结束后仍有记录，不能合并多个运行")
		}
		switch kind {
		case 0:
			if started {
				return r, fmt.Errorf("重复 TCC Start")
			}
			started = true
		case 10:
			params := strings.Fields(fields[1])
			if len(params) < 2 {
				return r, fmt.Errorf("journal 第%d行 TC Start 参数无效", line)
			}
			activity, err := strconv.Atoi(params[0])
			if err != nil || activity < 0 {
				return r, fmt.Errorf("journal 第%d行 activity 无效", line)
			}
			cases[activity] = params[1]
		case 900:
			if !started {
				return r, fmt.Errorf("TCC End 没有起始记录")
			}
			r.RunComplete = true
		case 220:
			if !started {
				return r, fmt.Errorf("TP Result 没有起始记录")
			}
			params := strings.Fields(fields[1])
			if len(params) < 3 {
				return r, fmt.Errorf("journal 第%d行缺少 TP 参数", line)
			}
			values := [3]int{}
			for i := range values {
				n, err := strconv.Atoi(params[i])
				if err != nil || n < 0 {
					return r, fmt.Errorf("journal 第%d行 TP 参数无效", line)
				}
				values[i] = n
			}
			key := fmt.Sprintf("%d:%d", values[0], values[1])
			if seen[key] {
				return r, fmt.Errorf("重复 TP 结果 %s", key)
			}
			seen[key] = true
			label := "OTHER"
			if values[2] < len(names) {
				label = names[values[2]]
			}
			r.Counts[label]++
			if label != "OTHER" && strings.TrimSpace(fields[2]) != label {
				return r, fmt.Errorf("结果码%d与文本%s不一致", values[2], fields[2])
			}
			r.Results = append(r.Results, journalResult{Activity: values[0], TP: values[1], Code: values[2], Result: label, Case: cases[values[0]], Description: fields[2]})
		}
	}
	if err := scanner.Err(); err != nil {
		return r, fmt.Errorf("读取 journal: %w", err)
	}
	if len(r.Results) == 0 {
		return r, fmt.Errorf("journal 没有 TP Result，不能报告测试通过")
	}
	return r, nil
}
