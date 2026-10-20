package ledgercli

import "testing"

func TestRenderSeedsDirectiveAndTxn(t *testing.T) {
	rows := []Row{
		// pad 指令：两行对称归零，应还原成一行指令原文形态
		{Date: "2026-10-09", Account: "Assets:Securities:BrokerDemo", Flag: "P", Amount: "285.50", Currency: "CNY"},
		{Date: "2026-10-09", Account: "Income:Investment:EquityDemo", Flag: "P", Amount: "-285.50", Currency: "CNY"},
		// 普通交易：金额脱敏为 ±X.XX
		{Date: "2026-10-08", Payee: "DemoShop", Narration: "支出(餐饮): demo meal", Account: "Expenses:Food:DemoEat", Flag: "*", Amount: "16.25", Currency: "CNY"},
		{Date: "2026-10-08", Payee: "DemoShop", Narration: "支出(餐饮): demo meal", Account: "Liabilities:Card:DemoBank", Flag: "*", Amount: "-16.25", Currency: "CNY"},
	}
	got := RenderSeeds(GroupRows(rows, 3))
	want := "2026-10-09 pad Assets:Securities:BrokerDemo Income:Investment:EquityDemo\n" +
		"2026-10-08 DemoShop 支出(餐饮): demo meal\n" +
		"  Expenses:Food:DemoEat X.XX CNY\n" +
		"  Liabilities:Card:DemoBank -X.XX CNY\n"
	if got != want {
		t.Errorf("RenderSeeds:\n got=%q\nwant=%q", got, want)
	}
}
