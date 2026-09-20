// Package holiday 提供中国法定节假日（含调休补班）的工作日判断。
//
// 数据来源：国务院办公厅每年 10/11 月公布的次年部分节假日安排。当前内置
// 2026 年安排（2026-09-20 经 timor.tech 节假日接口逐日核对，与国办公告一致）。
// 次年安排公布后需在本文件补表（见 PLAN.md「后续迭代」年度维护项）；
// 无表年份 IsWorkday 回退「周一至周五为工作日」，不影响其他功能。
package holiday

import (
	"fmt"
	"time"
)

type yearTable struct {
	holidays map[string]struct{} // 放假日期（MM-DD，含调休拼假的连休日）
	workdays map[string]struct{} // 调休补班日期（周末上班）
}

var tables = map[int]*yearTable{}

func init() {
	t := newYearTable()
	// 元旦：1.1~1.3 放假，1.4（周日）补班
	t.addHolidays(1, 1, 3)
	t.addWorkday(1, 4)
	// 春节：2.15~2.23 放假，2.14、2.28（周六）补班
	t.addHolidays(2, 15, 23)
	t.addWorkday(2, 14)
	t.addWorkday(2, 28)
	// 清明节：4.4~4.6 放假
	t.addHolidays(4, 4, 6)
	// 劳动节：5.1~5.5 放假，5.9（周六）补班
	t.addHolidays(5, 1, 5)
	t.addWorkday(5, 9)
	// 端午节：6.19~6.21 放假
	t.addHolidays(6, 19, 21)
	// 中秋节：9.25~9.27 放假，9.20（周日）补班
	t.addHolidays(9, 25, 27)
	t.addWorkday(9, 20)
	// 国庆节：10.1~10.7 放假，10.10（周六）补班
	t.addHolidays(10, 1, 7)
	t.addWorkday(10, 10)
	tables[2026] = t
}

func newYearTable() *yearTable {
	return &yearTable{holidays: map[string]struct{}{}, workdays: map[string]struct{}{}}
}

func (t *yearTable) addHolidays(month, from, to int) {
	for d := from; d <= to; d++ {
		t.holidays[key(month, d)] = struct{}{}
	}
}

func (t *yearTable) addWorkday(month, day int) {
	t.workdays[key(month, day)] = struct{}{}
}

func key(month, day int) string {
	return fmt.Sprintf("%02d-%02d", month, day)
}

// HasYearData 报告该年份是否有内置节假日表。
func HasYearData(year int) bool {
	_, ok := tables[year]
	return ok
}

// IsWorkday 判断给定日期是否为工作日：法定假日返回 false，调休补班返回 true，
// 其余按周一至周五判断；年份无内置表时按周末规则回退。
func IsWorkday(t time.Time) bool {
	if tbl, ok := tables[t.Year()]; ok {
		k := key(int(t.Month()), t.Day())
		if _, is := tbl.holidays[k]; is {
			return false
		}
		if _, is := tbl.workdays[k]; is {
			return true
		}
	}
	switch t.Weekday() {
	case time.Saturday, time.Sunday:
		return false
	}
	return true
}

// LastWorkdayOfWeek 返回 t 所在周（周一~周日）的最后一个工作日（当天 12:00）；
// 整周均为法定假时返回零值。
func LastWorkdayOfWeek(t time.Time) time.Time {
	weekday := int(t.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	monday := time.Date(t.Year(), t.Month(), t.Day()-weekday+1, 12, 0, 0, 0, t.Location())
	last := time.Time{}
	for i := 0; i < 7; i++ {
		day := monday.AddDate(0, 0, i)
		if IsWorkday(day) {
			last = day
		}
	}
	return last
}

// FirstWorkdayOfMonth 返回 t 所在月份的第一个工作日（当天 12:00）；
// 整月均无工作日（理论情形）返回零值。
func FirstWorkdayOfMonth(t time.Time) time.Time {
	first := time.Date(t.Year(), t.Month(), 1, 12, 0, 0, 0, t.Location())
	for d := 0; d < 31; d++ {
		day := first.AddDate(0, 0, d)
		if day.Month() != first.Month() {
			break
		}
		if IsWorkday(day) {
			return day
		}
	}
	return time.Time{}
}
