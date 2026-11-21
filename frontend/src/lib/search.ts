import type { Master } from './types';

// 条件の種類。クエリの名前は、バックエンドの /api/recruits と同じ
export const KINDS = ['employment', 'area', 'station', 'occupation'] as const;
export type Kind = (typeof KINDS)[number];

// 画面に並べる順（しぼり込みのチップと、選んだ条件のチップ）。何の仕事か → どこか → 雇用の形、の順（求人サイトの検索画面に多い並び）。
// URL のクエリの順（上の KINDS）とは別。KINDS を変えると、共有された URL の形が変わる
export const DISPLAY_ORDER = ['occupation', 'area', 'station', 'employment'] as const satisfies readonly Kind[];

/** キーワードの長さの上限。バックエンドの /api/recruits の q と同じ値（超えると 422）。 */
export const KEYWORD_MAX_LENGTH = 100;

export type Selection = Record<Kind, number[]> & { q: string; page: number };

type RawParams = Record<string, string | string[] | undefined>;

function toInts(value: string | string[] | undefined): number[] {
  const list = Array.isArray(value) ? value : value === undefined ? [] : [value];
  const codes = list.map((v) => Number(v)).filter((n) => Number.isInteger(n) && n > 0);
  return [...new Set(codes)].sort((a, b) => a - b);
}

/** キーワードを整える。空白（全角を含む）の連なりを 1 つの半角の空白にし、前後を落とし、上限で切る。 */
export function normalizeKeyword(value: string | string[] | undefined): string {
  const first = Array.isArray(value) ? value[0] : value;
  return (first ?? '').replace(/\s+/g, ' ').trim().slice(0, KEYWORD_MAX_LENGTH);
}

/** URL のクエリを、検索の条件にする。数字でない値・重複は捨てる（API へ 422 になる値を渡さない）。 */
export function parseSelection(params: RawParams): Selection {
  const page = Number(Array.isArray(params.page) ? params.page[0] : params.page);
  return {
    employment: toInts(params.employment),
    area: toInts(params.area),
    station: toInts(params.station),
    occupation: toInts(params.occupation),
    q: normalizeKeyword(params.q),
    page: Number.isInteger(page) && page > 1 ? page : 1,
  };
}

/** 条件を、クエリ文字列（先頭の ? なし）にする。ページは 2 以上のときだけ入れる。 */
export function toQuery(sel: Selection): string {
  const q = new URLSearchParams();
  for (const kind of KINDS) for (const code of sel[kind]) q.append(kind, String(code));
  if (sel.q) q.set('q', sel.q);
  if (sel.page > 1) q.set('page', String(sel.page));
  return q.toString();
}

export function hrefFor(sel: Selection): string {
  const q = toQuery(sel);
  return q ? `/?${q}` : '/';
}

export function hasConditions(sel: Selection): boolean {
  return sel.q !== '' || KINDS.some((kind) => sel[kind].length > 0);
}

/** 条件のチップ。キーワードは kind が 'q'（code は 0）。 */
export type Chip = { kind: Kind | 'q'; code: number; label: string; removeHref: string };

/** 選んだ条件を、1 つずつ外せるチップにする。外すとページは 1 に戻る。 */
export function chipsFor(sel: Selection, master: Master): Chip[] {
  const names: Record<Kind, Map<number, string>> = {
    employment: new Map(master.employments.map((c) => [c.code, c.name])),
    area: new Map(master.areas.map((c) => [c.code, c.name])),
    station: new Map(master.stations.map((c) => [c.code, c.name])),
    occupation: new Map(master.occupations.flatMap((g) => g.items.map((c) => [c.code, c.name] as const))),
  };
  const chips: Chip[] = [];
  if (sel.q) chips.push({ kind: 'q', code: 0, label: `検索：${sel.q}`, removeHref: hrefFor({ ...sel, q: '', page: 1 }) });
  for (const kind of DISPLAY_ORDER) {
    for (const code of sel[kind]) {
      const label = names[kind].get(code);
      if (!label) continue; // マスタに無い（削除済みの）番号は出さない
      chips.push({
        kind,
        code,
        label,
        removeHref: hrefFor({ ...sel, [kind]: sel[kind].filter((c) => c !== code), page: 1 }),
      });
    }
  }
  return chips;
}
