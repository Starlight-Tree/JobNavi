import { describe, expect, it } from 'vitest';
import { chipsFor, DISPLAY_ORDER, hasConditions, hrefFor, KEYWORD_MAX_LENGTH, KINDS, normalizeKeyword, parseSelection, toQuery } from './search';
import type { Master } from './types';

const master: Master = {
  areas: [{ code: 3, name: '第一環' }],
  stations: [{ code: 1, name: '鹿児島本線' }],
  occupations: [{ category: '栽培', items: [{ code: 1, name: '無重力蔓の育成士' }] }],
  employments: [{ code: 1, name: '常勤' }],
};

describe('parseSelection', () => {
  it('数字でない値・0・重複を捨て、小さい順にそろえる', () => {
    const sel = parseSelection({ area: ['4', '3', 'x', '3', '0', '-2'], employment: '1', page: '3' });
    expect(sel.area).toEqual([3, 4]);
    expect(sel.employment).toEqual([1]);
    expect(sel.page).toBe(3);
  });

  it('page が壊れていれば 1 にする', () => {
    expect(parseSelection({ page: 'abc' }).page).toBe(1);
    expect(parseSelection({ page: '0' }).page).toBe(1);
    expect(parseSelection({ page: '1.5' }).page).toBe(1);
    expect(parseSelection({}).page).toBe(1);
  });
});

describe('toQuery / hrefFor', () => {
  it('条件が無ければ / だけ。page=1 は付けない', () => {
    expect(hrefFor(parseSelection({}))).toBe('/');
    expect(toQuery(parseSelection({ page: '1' }))).toBe('');
  });

  it('同じ種類は繰り返しの形で書く', () => {
    expect(toQuery(parseSelection({ area: ['3', '4'], employment: '1', page: '2' }))).toBe(
      'employment=1&area=3&area=4&page=2',
    );
  });

  it('hasConditions はページだけでは true にならない', () => {
    expect(hasConditions(parseSelection({ page: '4' }))).toBe(false);
    expect(hasConditions(parseSelection({ station: '1' }))).toBe(true);
  });
});

describe('chipsFor', () => {
  it('選んだ条件ごとに、その 1 つだけを外したリンクを作り、ページは 1 に戻す', () => {
    const sel = parseSelection({ area: '3', employment: '1', page: '2' });
    const chips = chipsFor(sel, master);
    // 並びは画面の順（職種 → エリア → 乗り場 → 雇用形態）。URL のクエリの順（KINDS）ではない
    expect(chips.map((c) => c.label)).toEqual(['第一環', '常勤']);
    expect(chips[0]?.removeHref).toBe('/?employment=1');
    expect(chips[1]?.removeHref).toBe('/?area=3');
  });

  it('画面の並び（DISPLAY_ORDER）は、条件の種類（KINDS）を 1 つずつ全部含み、URL の並びとは別', () => {
    expect([...DISPLAY_ORDER].sort()).toEqual([...KINDS].sort());
    expect([...DISPLAY_ORDER]).not.toEqual([...KINDS]);
  });

  it('マスタに無い番号はチップに出さない', () => {
    expect(chipsFor(parseSelection({ area: '99' }), master)).toEqual([]);
  });

  it('職種は区分の中から名前を引く', () => {
    expect(chipsFor(parseSelection({ occupation: '1' }), master).map((c) => c.label)).toEqual(['無重力蔓の育成士']);
  });
});

describe('キーワード（q）', () => {
  it('整える: 全角を含む空白の連なりは半角 1 つ、前後は落とし、上限で切る。配列は先頭だけ', () => {
    expect(normalizeKeyword('  温室　 夜勤 ')).toBe('温室 夜勤');
    expect(normalizeKeyword(['語り部', '別'])).toBe('語り部');
    expect(normalizeKeyword(undefined)).toBe('');
    expect(normalizeKeyword('あ'.repeat(KEYWORD_MAX_LENGTH + 20))).toHaveLength(KEYWORD_MAX_LENGTH);
  });

  it('URL の q を読み、クエリへ戻す（条件のあと・ページの前。空なら入れない）', () => {
    const sel = parseSelection({ q: '温室　夜勤', area: '3', page: '2' });
    expect(sel.q).toBe('温室 夜勤');
    expect(toQuery(sel)).toBe('area=3&q=%E6%B8%A9%E5%AE%A4+%E5%A4%9C%E5%8B%A4&page=2');
    expect(toQuery(parseSelection({ q: '   ' }))).toBe('');
  });

  it('hasConditions はキーワードだけでも true', () => {
    expect(hasConditions(parseSelection({ q: '温室' }))).toBe(true);
    expect(hasConditions(parseSelection({ q: ' ' }))).toBe(false);
  });

  it('キーワードのチップは先頭に出し、外すと q だけが消えてページは 1 に戻る', () => {
    const chips = chipsFor(parseSelection({ q: '語り部', area: '3', page: '2' }), master);
    expect(chips.map((c) => c.label)).toEqual(['検索：語り部', '第一環']);
    expect(chips[0]).toMatchObject({ kind: 'q', code: 0, removeHref: '/?area=3' });
    expect(chips[1]?.removeHref).toBe('/?q=%E8%AA%9E%E3%82%8A%E9%83%A8');
  });
});
