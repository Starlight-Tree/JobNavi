import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { parseSelection } from '../lib/search';
import type { Facets, Master } from '../lib/types';
import { FilterForm } from './FilterForm';

const push = vi.fn();
const replace = vi.fn();
vi.mock('next/navigation', () => ({ useRouter: () => ({ push, replace }) }));

const master: Master = {
  employments: [
    { code: 1, name: '常勤' },
    { code: 2, name: '短時間勤務' },
  ],
  areas: [
    { code: 3, name: '第一環' },
    { code: 4, name: '第二環' },
  ],
  stations: [{ code: 1, name: '鹿児島本線' }],
  occupations: [
    {
      category: '栽培',
      items: [
        { code: 1, name: '無重力蔓の育成士' },
        { code: 2, name: '人工夜の温室管理員' },
      ],
    },
    { category: '解説・案内', items: [{ code: 20, name: '案内所の係' }] },
  ],
};

const facets: Facets = {
  total: 11,
  employments: [
    { code: 1, count: 5 },
    { code: 2, count: 3 },
  ],
  areas: [{ code: 3, count: 2 }],
  stations: [{ code: 1, count: 2 }],
  occupations: [{ code: 1, count: 2 }],
};

beforeEach(() => {
  push.mockReset();
  replace.mockReset();
});

/** チップ（<summary>）。名前は「ラベル 値」。 */
const chip = (label: string) => screen.getByText(label, { selector: '.facet-label' }).closest('summary') as HTMLElement;
const facetOf = (label: string) => chip(label).closest('details') as HTMLDetailsElement;

describe('FilterForm（上の横並びのチップ）', () => {
  it('4 つのチップを並べ、選んでいなければ「すべて」と出す', () => {
    render(<FilterForm master={master} facets={facets} selection={parseSelection({})} />);
    for (const label of ['職種', 'エリア', '乗り場', '雇用形態']) {
      expect(chip(label)).toHaveTextContent(`${label}すべて`);
      expect(facetOf(label)).toHaveAttribute('data-active', 'false');
    }
  });

  it('チップは、職種 → エリア → 乗り場 → 雇用形態の順（キーワード欄のあと）', () => {
    render(<FilterForm master={master} facets={facets} selection={parseSelection({})} />);
    expect([...document.querySelectorAll('.facet-label')].map((e) => e.textContent)).toEqual(['職種', 'エリア', '乗り場', '雇用形態']);
    expect(document.querySelector('.filter-bar')?.firstElementChild).toHaveClass('filter-search');
  });

  it('選んだ条件は、チップに出して強調する（1 つならその名前、2 つ以上なら「先頭の名前 ほか N」）', () => {
    render(<FilterForm master={master} facets={facets} selection={parseSelection({ employment: '2', area: ['3', '4'], occupation: '20' })} />);
    expect(chip('雇用形態')).toHaveTextContent('短時間勤務');
    expect(chip('エリア')).toHaveTextContent('第一環ほか1');
    expect(chip('職種')).toHaveTextContent('案内所の係');
    expect(chip('乗り場')).toHaveTextContent('すべて');
    expect(facetOf('エリア')).toHaveAttribute('data-active', 'true');
    expect(facetOf('乗り場')).toHaveAttribute('data-active', 'false');
  });

  it('選択肢ごとの件数を出す。件数が無いものは 0 と出す', () => {
    render(<FilterForm master={master} facets={facets} selection={parseSelection({})} />);
    expect(screen.getByRole('checkbox', { name: '常勤', hidden: true }).closest('.option')).toHaveTextContent('5');
    expect(screen.getByRole('checkbox', { name: '第二環', hidden: true }).closest('.option')).toHaveTextContent('0');
  });

  it('チェックを入れると、その条件を足した URL へ移り、ページは 1 に戻す。チップの表示も変わる', async () => {
    render(<FilterForm master={master} facets={facets} selection={parseSelection({ area: '3', page: '2' })} />);
    await userEvent.click(chip('雇用形態'));
    await userEvent.click(screen.getByRole('checkbox', { name: '常勤' }));
    expect(push).toHaveBeenCalledWith('/?employment=1&area=3', { scroll: false });
    expect(screen.getByRole('checkbox', { name: '常勤' })).toBeChecked();
    expect(chip('雇用形態')).toHaveTextContent('常勤');
  });

  it('チェックを外すと、その条件だけを外した URL へ移る', async () => {
    render(<FilterForm master={master} facets={facets} selection={parseSelection({ area: ['3', '4'] })} />);
    await userEvent.click(chip('エリア'));
    await userEvent.click(screen.getByRole('checkbox', { name: '第一環' }));
    expect(push).toHaveBeenCalledWith('/?area=4', { scroll: false });
  });

  it('URL（選択）が外から変わったら、チェックとチップの表示を合わせ直す', () => {
    const { rerender } = render(<FilterForm master={master} facets={facets} selection={parseSelection({ area: '3' })} />);
    expect(screen.getByRole('checkbox', { name: '第一環', hidden: true })).toBeChecked();
    expect(chip('エリア')).toHaveTextContent('第一環');
    rerender(<FilterForm master={master} facets={facets} selection={parseSelection({})} />);
    expect(screen.getByRole('checkbox', { name: '第一環', hidden: true })).not.toBeChecked();
    expect(chip('エリア')).toHaveTextContent('すべて');
  });

  it('職種は、区分の見出しの下に、全部のチェックボックスを並べる（折りたたまない）', () => {
    render(<FilterForm master={master} facets={facets} selection={parseSelection({ occupation: '20' })} />);
    expect(document.querySelectorAll('.facet-panel details')).toHaveLength(0);
    const groups = [...document.querySelectorAll('.occupation-group')];
    expect(groups.map((g) => g.querySelector('.group-title')?.textContent)).toEqual(['栽培', '解説・案内']);
    expect(groups.map((g) => g.querySelectorAll('input[type="checkbox"]').length)).toEqual([2, 1]);
    expect(screen.getByRole('checkbox', { name: '案内所の係', hidden: true })).toBeChecked();
    expect(screen.getByRole('checkbox', { name: '無重力蔓の育成士', hidden: true })).not.toBeChecked();
  });

  it('JavaScript が無くても送れるよう、GET のフォームで name=種類・value=番号にしている', () => {
    render(<FilterForm master={master} facets={facets} selection={parseSelection({})} />);
    const form = screen.getByRole('form', { name: '求人のしぼり込み' });
    expect(form).toHaveAttribute('method', 'get');
    expect(screen.getByRole('checkbox', { name: '常勤', hidden: true })).toHaveAttribute('name', 'employment');
    expect(screen.getByRole('checkbox', { name: '第一環', hidden: true })).toHaveAttribute('value', '3');
  });

  it('JavaScript が動いているあいだは、送信ボタンを隠す（無いときだけ、ボタンで送る）', () => {
    render(<FilterForm master={master} facets={facets} selection={parseSelection({})} />);
    expect(screen.getByText('この条件でさがす', { selector: 'button' })).toHaveAttribute('hidden');
  });

  describe('一覧の開閉', () => {
    it('別のチップを開くと、前に開いていたチップは閉じる', async () => {
      render(<FilterForm master={master} facets={facets} selection={parseSelection({})} />);
      await userEvent.click(chip('雇用形態'));
      await waitFor(() => expect(facetOf('雇用形態').open).toBe(true));
      await userEvent.click(chip('エリア'));
      await waitFor(() => expect(facetOf('エリア').open).toBe(true));
      await waitFor(() => expect(facetOf('雇用形態').open).toBe(false));
    });

    it('外を押すと閉じる。一覧の中を押しても閉じない', async () => {
      render(<FilterForm master={master} facets={facets} selection={parseSelection({})} />);
      await userEvent.click(chip('雇用形態'));
      await waitFor(() => expect(facetOf('雇用形態').open).toBe(true));
      await userEvent.click(screen.getByRole('checkbox', { name: '短時間勤務' }));
      expect(facetOf('雇用形態').open).toBe(true);
      await userEvent.click(document.body);
      expect(facetOf('雇用形態').open).toBe(false);
    });

    it('フォーカスが一覧の外へ出たら閉じる。一覧の中でのフォーカスの移動では閉じない', async () => {
      render(<FilterForm master={master} facets={facets} selection={parseSelection({})} />);
      await userEvent.click(chip('エリア'));
      await waitFor(() => expect(facetOf('エリア').open).toBe(true));
      const first = screen.getByRole('checkbox', { name: '第一環' });
      const second = screen.getByRole('checkbox', { name: '第二環' });
      fireEvent.focusOut(first, { relatedTarget: second });
      expect(facetOf('エリア').open).toBe(true);
      fireEvent.focusOut(second, { relatedTarget: document.body });
      expect(facetOf('エリア').open).toBe(false);
    });

    it('ウィンドウを離れただけ（移り先なし）では閉じない', async () => {
      render(<FilterForm master={master} facets={facets} selection={parseSelection({})} />);
      await userEvent.click(chip('エリア'));
      await waitFor(() => expect(facetOf('エリア').open).toBe(true));
      fireEvent.focusOut(screen.getByRole('checkbox', { name: '第一環' }), { relatedTarget: null });
      expect(facetOf('エリア').open).toBe(true);
    });

    it('ほかの処理が先に受けた Esc（preventDefault 済み）では、閉じない', async () => {
      render(<FilterForm master={master} facets={facets} selection={parseSelection({})} />);
      await userEvent.click(chip('エリア'));
      await waitFor(() => expect(facetOf('エリア').open).toBe(true));
      const event = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true });
      event.preventDefault();
      screen.getByRole('checkbox', { name: '第一環' }).dispatchEvent(event);
      expect(facetOf('エリア').open).toBe(true);
    });

    it('画面の幅が変わったら閉じる', async () => {
      render(<FilterForm master={master} facets={facets} selection={parseSelection({})} />);
      await userEvent.click(chip('エリア'));
      await waitFor(() => expect(facetOf('エリア').open).toBe(true));
      fireEvent(window, new Event('resize'));
      expect(facetOf('エリア').open).toBe(false);
    });

    it('開いた一覧が画面の右へはみ出すときだけ、チップの右端にそろえる（data-align）', async () => {
      const rect = (right: number) => ({ right, left: 0, top: 0, bottom: 0, width: 0, height: 0, x: 0, y: 0, toJSON: () => ({}) }) as DOMRect;
      const spy = vi.spyOn(Element.prototype, 'getBoundingClientRect');
      render(<FilterForm master={master} facets={facets} selection={parseSelection({})} />);
      Object.defineProperty(document.documentElement, 'clientWidth', { configurable: true, value: 1000 });
      spy.mockReturnValue(rect(1100));
      await userEvent.click(chip('エリア'));
      await waitFor(() => expect(facetOf('エリア')).toHaveAttribute('data-align', 'end'));
      // 収まる幅なら付けない（開き直したとき）
      await userEvent.click(chip('エリア'));
      await waitFor(() => expect(facetOf('エリア').open).toBe(false));
      spy.mockReturnValue(rect(500));
      await userEvent.click(chip('エリア'));
      await waitFor(() => expect(facetOf('エリア').open).toBe(true));
      expect(facetOf('エリア')).not.toHaveAttribute('data-align');
      delete (document.documentElement as unknown as Record<string, unknown>).clientWidth;
    });

    it('Esc で閉じて、フォーカスをチップへ戻す', async () => {
      render(<FilterForm master={master} facets={facets} selection={parseSelection({})} />);
      await userEvent.click(chip('エリア'));
      await waitFor(() => expect(facetOf('エリア').open).toBe(true));
      await userEvent.click(screen.getByRole('checkbox', { name: '第一環' }));
      await userEvent.keyboard('{Escape}');
      expect(facetOf('エリア').open).toBe(false);
      expect(chip('エリア')).toHaveFocus();
    });
  });
  describe('キーワードの入力欄', () => {
    const box = () => screen.getByRole('searchbox', { name: /キーワード/ });

    it('先頭に 1 つ出し、URL の q を入れておく（上限の長さを付ける）', () => {
      render(<FilterForm master={master} facets={facets} selection={parseSelection({ q: '語り部' })} />);
      expect(box()).toHaveValue('語り部');
      expect(box()).toHaveAttribute('maxlength', '100');
      expect(box()).toHaveAttribute('name', 'q');
    });

    it('打つと、少し待ってから、履歴を増やさずに（replace）整えたキーワードで絞る。1 ページ目へ戻す', () => {
      vi.useFakeTimers();
      try {
        render(<FilterForm master={master} facets={facets} selection={parseSelection({ area: '3', page: '2' })} />);
        fireEvent.change(box(), { target: { value: '温室　夜勤 ' } });
        expect(replace).not.toHaveBeenCalled();
        vi.advanceTimersByTime(199);
        expect(replace).not.toHaveBeenCalled();
        vi.advanceTimersByTime(1);
        expect(replace).toHaveBeenCalledTimes(1);
        expect(replace).toHaveBeenCalledWith('/?area=3&q=%E6%B8%A9%E5%AE%A4+%E5%A4%9C%E5%8B%A4', { scroll: false });
        expect(push).not.toHaveBeenCalled();
      } finally {
        vi.useRealTimers();
      }
    });

    it('続けて打つと、待ちをやり直す（最後の 1 回だけ）', () => {
      vi.useFakeTimers();
      try {
        render(<FilterForm master={master} facets={facets} selection={parseSelection({})} />);
        fireEvent.change(box(), { target: { value: '温' } });
        vi.advanceTimersByTime(150);
        fireEvent.change(box(), { target: { value: '温室' } });
        vi.advanceTimersByTime(150);
        expect(replace).not.toHaveBeenCalled();
        vi.advanceTimersByTime(50);
        expect(replace).toHaveBeenCalledTimes(1);
        expect(replace).toHaveBeenCalledWith('/?q=%E6%B8%A9%E5%AE%A4', { scroll: false });
      } finally {
        vi.useRealTimers();
      }
    });

    it('日本語の変換中は絞らず、確定したら、待たずにすぐ絞る（1 回だけ）', () => {
      vi.useFakeTimers();
      try {
        render(<FilterForm master={master} facets={facets} selection={parseSelection({})} />);
        fireEvent.compositionStart(box());
        fireEvent.change(box(), { target: { value: 'おん' } });
        vi.advanceTimersByTime(1000);
        expect(replace).not.toHaveBeenCalled();
        fireEvent.compositionEnd(box(), { target: { value: '温室' } });
        expect(replace).toHaveBeenCalledTimes(1);
        expect(replace).toHaveBeenCalledWith('/?q=%E6%B8%A9%E5%AE%A4', { scroll: false });
        // ブラウザーによっては、確定のあとに input が来る。同じ検索を、もう一度送らない
        fireEvent.change(box(), { target: { value: '温室' } });
        vi.advanceTimersByTime(1000);
        expect(replace).toHaveBeenCalledTimes(1);
      } finally {
        vi.useRealTimers();
      }
    });

    it('変換の前に予約した検索は、変換を始めたら取り消す。変換をやめて元に戻したら、何も送らない', () => {
      vi.useFakeTimers();
      try {
        render(<FilterForm master={master} facets={facets} selection={parseSelection({})} />);
        fireEvent.change(box(), { target: { value: 'ab' } });
        vi.advanceTimersByTime(100);
        fireEvent.compositionStart(box());
        vi.advanceTimersByTime(1000);
        expect(replace).not.toHaveBeenCalled();
        fireEvent.compositionEnd(box(), { target: { value: '' } });
        vi.advanceTimersByTime(1000);
        expect(replace).not.toHaveBeenCalled();
      } finally {
        vi.useRealTimers();
      }
    });

    it('入力欄を触っている間は、戻ってきた古い URL で、続けて打った文字を消さない。離れたあとの URL の変更には合わせる', () => {
      const sel = (q: string) => parseSelection(q ? { q } : {});
      const { rerender } = render(<FilterForm master={master} facets={facets} selection={sel('')} />);
      box().focus();
      fireEvent.change(box(), { target: { value: '温' } });
      fireEvent.change(box(), { target: { value: '温室' } });
      rerender(<FilterForm master={master} facets={facets} selection={sel('温')} />); // 「温」の検索の戻り
      expect(box()).toHaveValue('温室');
      box().blur();
      rerender(<FilterForm master={master} facets={facets} selection={sel('')} />); // チップの ✕ など、外からの変更
      expect(box()).toHaveValue('');
    });

    it('Enter（送信）は、待たずにすぐ絞る（push）。待っていた replace は出さない', () => {
      vi.useFakeTimers();
      try {
        render(<FilterForm master={master} facets={facets} selection={parseSelection({})} />);
        fireEvent.change(box(), { target: { value: '語り部' } });
        fireEvent.submit(box().closest('form') as HTMLFormElement);
        expect(push).toHaveBeenCalledWith('/?q=%E8%AA%9E%E3%82%8A%E9%83%A8', { scroll: false });
        vi.advanceTimersByTime(1000);
        expect(replace).not.toHaveBeenCalled();
      } finally {
        vi.useRealTimers();
      }
    });

    it('キーワードを入れたまま、チェックを入れても、キーワードは残る', async () => {
      render(<FilterForm master={master} facets={facets} selection={parseSelection({ q: '語り部' })} />);
      await userEvent.click(chip('エリア'));
      await userEvent.click(screen.getByRole('checkbox', { name: '第一環' }));
      expect(push).toHaveBeenCalledWith('/?area=3&q=%E8%AA%9E%E3%82%8A%E9%83%A8', { scroll: false });
    });

    it('URL が外から変わったら（チップの ✕・クリア・戻る）、入力欄も合わせる。打っている途中の末尾の空白は消さない', () => {
      const { rerender } = render(<FilterForm master={master} facets={facets} selection={parseSelection({ q: '語り部' })} />);
      rerender(<FilterForm master={master} facets={facets} selection={parseSelection({})} />);
      expect(box()).toHaveValue('');
      fireEvent.change(box(), { target: { value: '温室 ' } });
      rerender(<FilterForm master={master} facets={facets} selection={parseSelection({ q: '温室' })} />);
      expect(box()).toHaveValue('温室 ');
    });
  });
});
