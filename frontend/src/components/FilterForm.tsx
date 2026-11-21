'use client';

import { useRouter } from 'next/navigation';
import { useEffect, useRef, useState, useTransition, type ReactNode } from 'react';
import { DISPLAY_ORDER, hrefFor, KEYWORD_MAX_LENGTH, KINDS, normalizeKeyword, type Kind, type Selection } from '../lib/search';
import type { Code, Facets, Master } from '../lib/types';

type Props = { master: Master; facets: Facets; selection: Selection };

const FACET_KEY: Record<Kind, keyof Omit<Facets, 'total'>> = {
  employment: 'employments',
  area: 'areas',
  station: 'stations',
  occupation: 'occupations',
};

// チップの名前。短い名詞にそろえる（「乗り場」は、駅・停留所をまとめた、この世界の言い方）
const TITLE: Record<Kind, string> = { occupation: '職種', area: 'エリア', station: '乗り場', employment: '雇用形態' };

// 打つのをやめてから絞り込むまでの待ち。Algolia の案内は 200ms（300ms を超えると、待たされると感じ始める）。日本語の変換の確定は、待たずにすぐ絞る
const TYPING_DELAY_MS = 200;

function counts(facets: Facets, kind: Kind): Map<number, number> {
  return new Map(facets[FACET_KEY[kind]].map((f) => [f.code, f.count]));
}

// サーバーから来た選択（URL）と同じかを見分けるための鍵
function keyOf(sel: Selection): string {
  return [...KINDS.map((k) => `${k}:${sel[k].join(',')}`), `q:${sel.q}`].join('|');
}

/**
 * 「ラベル 値 ▾」のチップ（2 つ以上選ぶと「ほか N」を添える）と、その真下に開く選択肢の一覧（DocuNavi の資料一覧の絞り込みと同じ形。複数選べる点だけ違う）。
 * 開閉は、ブラウザー標準の <details> に任せる（JavaScript が無くても開き、チェックを入れて送れる）。
 * 外を押す・Esc・別のチップを開く、で閉じるのは、FilterForm の側が受ける。
 */
function Facet({ label, names, children }: { label: string; names: string[]; children: ReactNode }) {
  const active = names.length > 0;
  return (
    <details className="facet" data-active={active}>
      <summary className="facet-summary" title={active ? `${label}：${names.join('、')}` : undefined}>
        <span className="facet-label">{label}</span>
        {/* 選んでいなければ「すべて」、1 つ以上ならその先頭の名前（長ければ「…」で省略）。2 つ目以降は、省略されないよう、件数を別に出す */}
        <span className="facet-value">{names[0] ?? 'すべて'}</span>
        {names.length > 1 ? <span className="facet-more">ほか{names.length - 1}</span> : null}
        <span className="facet-caret" aria-hidden="true">
          ▾
        </span>
      </summary>
      <div className="facet-panel">{children}</div>
    </details>
  );
}

export function FilterForm({ master, facets, selection }: Props) {
  const router = useRouter();
  const [pending, startTransition] = useTransition();
  const formRef = useRef<HTMLFormElement>(null);
  // JavaScript が動いているあいだは、チェックを入れた時点で絞り込むので、送信ボタンは隠す（JavaScript が無いときだけ、ボタンで送る）
  const [enhanced, setEnhanced] = useState(false);
  useEffect(() => setEnhanced(true), []);
  // 画面の選択は、チェックを入れた瞬間に変える（サーバーの再描画を待たない）。
  // URL が別の経路（チップの ✕、クリア、戻る）で変わったときは、サーバーの選択に合わせ直す。
  const [chosen, setChosen] = useState(selection);
  const [seen, setSeen] = useState(keyOf(selection));
  // 自分が URL へ送ったキーワードで、まだ戻ってきていないもの（古い順）。URL が変わったとき、その q がここにあれば「自分が送った検索の戻り」、
  // 無ければ「外からの移動」（戻る・進む・チップの ✕・クリア）と見分ける。
  // 戻りは、続けて打った手元の文字を上書きしない（上書きすると、「温」→「温室」と打つと、一瞬「温」へ戻る。実測）。外からの移動には、入力欄も合わせる。
  // 入力欄にカーソルがあるかは、見分けに使わない（カーソルがあるまま戻る・進むを押しても、URL に合う）
  const sent = useRef<string[]>([]);
  // キーワードの入力欄は、手元の文字を持つ（日本語の変換中の文字が、URL の往復で崩れないように。DocuNavi の検索欄と同じ）
  const [text, setText] = useState(selection.q);
  if (keyOf(selection) !== seen) {
    setSeen(keyOf(selection));
    const mine = sent.current.includes(selection.q);
    setChosen(mine ? { ...selection, q: chosen.q } : selection);
    // 打っている途中の文字（末尾の空白など）は、整えた形が URL と同じなら、そのまま残す
    if (!mine && normalizeKeyword(text) !== selection.q) setText(selection.q);
  }
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const composing = useRef(false);
  // 最後に URL へ送った（または、外からの移動で URL から受けた）キーワード。同じ検索を二重に送らない（確定のイベントが重なっても 1 回）
  const lastSent = useRef(selection.q);
  // URL へ送ったキーワードを記録する。戻ってこなかった送信（通信の失敗など）が溜まらないよう、10 件まで
  function record(q: string) {
    lastSent.current = q;
    sent.current = [...sent.current, q].slice(-10);
  }
  // URL が変わったあとの片づけ。自分が送った検索の戻りなら、それより古い送信は、もう戻ってこないので捨てる。
  // 外からの移動なら、予約していた検索（古い手元の文字）を取り消す。取り消さないと、戻った直後に発火して、元の条件へ戻してしまう
  useEffect(() => {
    const at = sent.current.indexOf(selection.q);
    if (at >= 0) {
      sent.current = sent.current.slice(at + 1);
    } else {
      sent.current = [];
      clearTimeout(timer.current);
      lastSent.current = selection.q;
    }
  }, [keyOf(selection)]);
  useEffect(() => () => clearTimeout(timer.current), []);

  // チップの一覧を、外を押す・フォーカスが外へ出る・Esc・別のチップを開く・画面の幅が変わる、で閉じる。Esc のときは、開いていたチップへフォーカスを戻す。
  // 開いたとき、一覧が画面の右へはみ出すなら、チップの右端にそろえて開く（チップの位置は、幅によって変わる）
  useEffect(() => {
    const form = formRef.current;
    if (!form) return;
    const menus = () => [...form.querySelectorAll<HTMLDetailsElement>('details.facet')];
    const closeAll = () => {
      for (const d of menus()) d.open = false;
    };
    const onPointer = (event: MouseEvent) => {
      for (const d of menus()) if (d.open && !d.contains(event.target as Node)) d.open = false;
    };
    const onFocusOut = (event: FocusEvent) => {
      const d = (event.target as Element).closest<HTMLDetailsElement>('details.facet');
      const next = event.relatedTarget as Node | null;
      // 移り先が分かるとき（Tab・クリックで別の要素へ）だけ。ウィンドウを離れたとき（移り先なし）は、開いたままにする
      if (d?.open && next && !d.contains(next)) d.open = false;
    };
    const onKey = (event: KeyboardEvent) => {
      // アカウントのメニューなど、ほかの Esc の処理が先に受けたときは、何もしない（フォーカスを奪わない）
      if (event.key !== 'Escape' || event.defaultPrevented) return;
      const opened = menus().find((d) => d.open);
      if (!opened) return;
      opened.open = false;
      opened.querySelector<HTMLElement>('summary')?.focus();
    };
    const onToggle = (event: Event) => {
      const target = event.target as HTMLElement;
      if (!(target instanceof HTMLDetailsElement) || !target.classList.contains('facet') || !target.open) return;
      for (const d of menus()) if (d !== target && d.open) d.open = false;
      delete target.dataset.align;
      const panel = target.querySelector<HTMLElement>('.facet-panel');
      if (panel && panel.getBoundingClientRect().right > document.documentElement.clientWidth - 8) target.dataset.align = 'end';
    };
    document.addEventListener('mousedown', onPointer);
    document.addEventListener('keydown', onKey);
    window.addEventListener('resize', closeAll);
    form.addEventListener('focusout', onFocusOut);
    form.addEventListener('toggle', onToggle, true);
    return () => {
      document.removeEventListener('mousedown', onPointer);
      document.removeEventListener('keydown', onKey);
      window.removeEventListener('resize', closeAll);
      form.removeEventListener('focusout', onFocusOut);
      form.removeEventListener('toggle', onToggle, true);
    };
  }, []);

  // 打つたびに履歴を増やさない（replace）。打ち止めから少し待って絞り込む。変換中は待つ（途中の文字では絞らない）。
  // 変換の確定（immediate）と Enter（送信）は、待たずにすぐ絞り込む
  function typeKeyword(value: string, immediate = false) {
    setText(value);
    const next: Selection = { ...chosen, q: normalizeKeyword(value), page: 1 };
    setChosen(next);
    clearTimeout(timer.current);
    if (composing.current || next.q === lastSent.current) return;
    const go = () => {
      record(next.q);
      startTransition(() => router.replace(hrefFor(next), { scroll: false }));
    };
    if (immediate) go();
    else timer.current = setTimeout(go, TYPING_DELAY_MS);
  }

  function toggle(kind: Kind, code: number, on: boolean) {
    const codes = on ? [...chosen[kind], code] : chosen[kind].filter((c) => c !== code);
    const next: Selection = { ...chosen, [kind]: [...new Set(codes)].sort((a, b) => a - b), page: 1 };
    setChosen(next);
    // この URL には、最新のキーワードも入っている。予約していたキーワードの検索は要らない（古い条件で上書きしてしまう）
    clearTimeout(timer.current);
    record(next.q);
    startTransition(() => router.push(hrefFor(next), { scroll: false }));
  }

  function option(kind: Kind, item: Code, count: Map<number, number>) {
    const n = count.get(item.code) ?? 0;
    const checked = chosen[kind].includes(item.code);
    const id = `${kind}-${item.code}`;
    return (
      <div key={item.code} className={`option${n === 0 && !checked ? ' option-empty' : ''}`}>
        <input
          type="checkbox"
          id={id}
          name={kind}
          value={item.code}
          checked={checked}
          onChange={(e) => toggle(kind, item.code, e.target.checked)}
        />
        <label htmlFor={id}>{item.name}</label>
        <span className="count">
          {n}
          <span className="sr-only"> 件</span>
        </span>
      </div>
    );
  }

  const nameOf = (kind: Kind): string[] => {
    const all: Code[] =
      kind === 'occupation' ? master.occupations.flatMap((g) => g.items) : kind === 'employment' ? master.employments : kind === 'area' ? master.areas : master.stations;
    return chosen[kind].flatMap((code) => all.find((c) => c.code === code)?.name ?? []);
  };

  const occupationCounts = counts(facets, 'occupation');
  const itemsOf = (kind: Exclude<Kind, 'occupation'>): Code[] => (kind === 'employment' ? master.employments : kind === 'area' ? master.areas : master.stations);

  return (
    <form
      ref={formRef}
      className="filter-bar"
      method="get"
      action="/"
      aria-label="求人のしぼり込み"
      aria-busy={pending}
      onSubmit={(e) => {
        e.preventDefault();
        clearTimeout(timer.current);
        record(chosen.q);
        startTransition(() => router.push(hrefFor({ ...chosen, page: 1 }), { scroll: false }));
      }}
    >
      <input
        type="search"
        name="q"
        className="filter-search"
        value={text}
        maxLength={KEYWORD_MAX_LENGTH}
        placeholder="キーワードで探す…"
        aria-label="キーワード（空白で区切ると、すべてを含む求人だけ）"
        title="空白で区切ると、すべてを含む求人だけに絞ります（AND）"
        enterKeyHint="search"
        onChange={(e) => typeKeyword(e.target.value)}
        onCompositionStart={() => {
          composing.current = true;
          clearTimeout(timer.current); // 変換の前に予約した検索は、変換中に動かさない
        }}
        onCompositionEnd={(e) => {
          composing.current = false;
          typeKeyword(e.currentTarget.value, true);
        }}
      />

      {/* チップは DISPLAY_ORDER の順。職種は、区分の見出しの下に、全部のチェックボックスを並べる（折りたたまない。一覧の高さを超えたら、中でスクロール）。幅は、いちばん長い職種名に合う */}
      {DISPLAY_ORDER.map((kind) => {
        if (kind === 'occupation') {
          return (
            <Facet key={kind} label={TITLE[kind]} names={nameOf(kind)}>
              <fieldset>
                <legend className="sr-only">{TITLE[kind]}</legend>
                {master.occupations.map((group) => (
                  <div key={group.category} className="occupation-group" role="group" aria-label={group.category}>
                    <p className="group-title" aria-hidden="true">
                      {group.category}
                    </p>
                    {group.items.map((item) => option(kind, item, occupationCounts))}
                  </div>
                ))}
              </fieldset>
            </Facet>
          );
        }
        const count = counts(facets, kind);
        return (
          <Facet key={kind} label={TITLE[kind]} names={nameOf(kind)}>
            <fieldset>
              <legend className="sr-only">{TITLE[kind]}</legend>
              {itemsOf(kind).map((item) => option(kind, item, count))}
            </fieldset>
          </Facet>
        );
      })}

      <button type="submit" className="filter-submit" hidden={enhanced}>
        この条件でさがす
      </button>
      <p className="filter-status" role="status">
        {pending ? '更新しています…' : ''}
      </p>
    </form>
  );
}
