package ledgercli

import (
	"reflect"
	"strconv"
	"testing"
)

// 测试夹具一律用合成占位数据（甲店/乙平台/2000-01-xx），禁止从真实账本搬运商户名与日期。

func txnSeed(date, payee, narration string, postings int) TxnSeed {
	ps := make([]Row, postings)
	for i := range ps {
		ps[i] = Row{Date: date, Payee: payee, Narration: narration, Account: "Assets:A" + strconv.Itoa(i)}
	}
	return TxnSeed{Date: date, Payee: payee, Narration: narration, Postings: ps}
}

func seedID(g TxnSeed) string {
	return g.Date + "|" + g.Payee + "|" + g.Narration + "|" + strconv.Itoa(len(g.Postings))
}

func TestMergeKeywordLists(t *testing.T) {
	tests := []struct {
		name  string
		lists [][]TxnSeed
		want  []string
	}{
		{
			// 老的全命中记录（2000-01-02，双词覆盖）必须排在新单命中（2000-01-05，单词）之前
			name:  "命中词数多的提顶",
			lists: [][]TxnSeed{{txnSeed("2000-01-05", "甲店", "摘要A", 1), txnSeed("2000-01-02", "乙平台", "摘要B", 2)}, {txnSeed("2000-01-02", "乙平台", "摘要B", 1)}},
			want:  []string{"2000-01-02|乙平台|摘要B|2", "2000-01-05|甲店|摘要A|1"},
		},
		{
			name:  "覆盖相同按词序回填",
			lists: [][]TxnSeed{{txnSeed("2000-02-01", "词一", "n", 1)}, {txnSeed("2000-03-01", "词二", "n", 1)}},
			want:  []string{"2000-02-01|词一|n|1", "2000-03-01|词二|n|1"},
		},
		{
			name:  "同键取全量分录且同列表重复只计一次覆盖",
			lists: [][]TxnSeed{{txnSeed("2000-04-01", "p", "n", 1), txnSeed("2000-04-01", "p", "n", 2)}, {txnSeed("2000-04-01", "p", "n", 1)}},
			want:  []string{"2000-04-01|p|n|2"},
		},
		{
			name:  "空列表退化",
			lists: [][]TxnSeed{{}, {txnSeed("2000-05-01", "p", "n", 1)}, nil},
			want:  []string{"2000-05-01|p|n|1"},
		},
		{
			name:  "全空",
			lists: [][]TxnSeed{{}, nil},
			want:  nil,
		},
	}
	for _, tt := range tests {
		got := mergeKeywordLists(tt.lists)
		var ids []string
		for _, g := range got {
			ids = append(ids, seedID(g))
		}
		var want []string
		if len(tt.want) > 0 {
			want = tt.want
		}
		if !reflect.DeepEqual(ids, want) {
			t.Errorf("%s: got=%v want=%v", tt.name, ids, want)
		}
	}
}
