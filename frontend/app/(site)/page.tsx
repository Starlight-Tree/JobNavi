import Link from 'next/link';
import { redirect } from 'next/navigation';
import { FilterForm } from '../../src/components/FilterForm';
import { PagerBar } from '../../src/components/PagerBar';
import { RecommendedRecruits } from '../../src/components/RecommendedRecruits';
import { SelectableCard } from '../../src/components/SelectableCard';
import { SelectedChips } from '../../src/components/SelectedChips';
import { fetchFacets, fetchMaster, fetchRecruits } from '../../src/lib/api';
import { loadRecommendations } from '../../src/lib/load-related';
import { chipsFor, hasConditions, hrefFor, parseSelection, toQuery } from '../../src/lib/search';
import type { Facets, Master, RecruitPage, RecruitSummary } from '../../src/lib/types';

export const dynamic = 'force-dynamic';

type Props = { searchParams: Promise<Record<string, string | string[] | undefined>> };

export default async function Home({ searchParams }: Props) {
  const selection = parseSelection(await searchParams);

  // 「あなたへのおすすめ」は、検索条件（キーワードを含む）が付いていない 1 ページ目だけ。ログインしていなければ、読み込みの中で API を呼ばず []（失敗しても []。一覧は壊さない）
  const showRecommendations = !hasConditions(selection) && selection.page === 1;

  let data: { master: Master; facets: Facets; result: RecruitPage; recommended: RecruitSummary[] } | null = null;
  try {
    const [master, facets, result, recommended] = await Promise.all([
      fetchMaster(),
      fetchFacets(selection),
      fetchRecruits(selection),
      showRecommendations ? loadRecommendations() : Promise.resolve([]),
    ]);
    data = { master, facets, result, recommended };
  } catch {
    // 例外の文面は画面に出さない（API のアドレスなどが混ざるため）。固定の文だけ出す
    data = null;
  }

  if (data === null) {
    return (
      <main className="page">
        <h1>宇宙の植物園・ガーデンの求人</h1>
        <div className="notice" role="alert">
          <p>求人を読み込めませんでした。しばらくしてから、もう一度開いてください。</p>
        </div>
      </main>
    );
  }

  const { master, facets, result, recommended } = data;
  // 最後のページより先を開いたら、最後のページへ移す
  if (result.total > 0 && result.page > result.last_page) {
    redirect(hrefFor({ ...selection, page: result.last_page }));
  }

  const chips = chipsFor(selection, master);
  const query = toQuery(selection);

  return (
    <main className="page">
      <div className="title-row">
        <h1>宇宙の植物園・ガーデンの求人</h1>
      </div>

      <p className="diagnosis-hint">
        どれを選べばいいか迷ったら、<Link href="/diagnosis">3 つの質問で診断</Link>してみてください。
      </p>

      {/* 条件バー: しぼり込みと件数。1024px 以上では、スクロールしても画面の上に留まる（globals.css の .search-bar）。件数の書き方は DocuNavi の資料一覧と同じ（「全N件」「検索結果 N件」） */}
      <div className="search-bar">
        <FilterForm master={master} facets={facets} selection={selection} />
        <p className="result-count" aria-live="polite">
          {hasConditions(selection) ? '検索結果 ' : '全'}
          {result.total}件
        </p>
      </div>

      {/* ログインした求職者で、履歴があるときだけ（無ければ、何も出ない）。一覧の上の控えめな帯 */}
      <RecommendedRecruits jobs={recommended} />

      <section className="results" aria-label="検索結果">
        <SelectedChips chips={chips} />
        {result.items.length > 0 ? (
          <>
            {result.items.map((job) => (
              <SelectableCard key={job.id} job={job} query={query} />
            ))}
            <PagerBar selection={selection} last={result.last_page} total={result.total} perPage={result.per_page} />
          </>
        ) : (
          <div className="empty">
            <h2>条件に合う求人は見つかりませんでした</h2>
            {hasConditions(selection) ? (
              <>
                <p>条件を 1 つ外すと、見つかることがあります。上のチップの ✕ で、条件を 1 つずつ外せます。</p>
                <a className="primary-button inline" href="/">
                  条件をすべてクリア
                </a>
              </>
            ) : (
              <p>いま公開している求人はありません。</p>
            )}
          </div>
        )}
      </section>
    </main>
  );
}
