import { render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '../../src/lib/api';
import type { RecruitSummary } from '../../src/lib/types';

// 一覧（トップ）の「あなたへのおすすめ」の出し分け。API・ログイン・しぼり込みの部品は差し替える（実 API・ネットワークは呼ばない）
const api = vi.hoisted(() => ({ fetchMaster: vi.fn(), fetchFacets: vi.fn(), fetchRecruits: vi.fn(), fetchRecommendations: vi.fn() }));
vi.mock('../../src/lib/api', async (importOriginal) => ({ ...(await importOriginal<typeof import('../../src/lib/api')>()), ...api }));
const session = vi.hoisted(() => ({ token: null as string | null }));
vi.mock('../../src/lib/session', () => ({ userToken: async () => session.token }));
vi.mock('next/navigation', () => ({
  usePathname: () => '/',
  redirect: (to: string) => {
    throw new Error(`REDIRECT ${to}`);
  },
}));
vi.mock('../../src/components/FilterForm', () => ({ FilterForm: () => <div data-testid="filter-form" /> }));

const { default: Home } = await import('./page');

function job(id: number): RecruitSummary {
  return {
    id,
    corporate: '星庭ワークス',
    category: `職種 ${id}`,
    salary: 2,
    pay_min: 180000,
    pay_max: 240000,
    comment1: null,
    note1: null,
    formfile: null,
    areas: ['北の丘'],
    stations: [],
    occupations: [],
    employments: ['正社員'],
    hero_image: null,
  };
}

const params = (p: Record<string, string> = {}) => ({ searchParams: Promise.resolve(p) });
const band = () => screen.queryByRole('region', { name: 'あなたへのおすすめ' });

beforeEach(() => {
  for (const fn of Object.values(api)) fn.mockReset();
  session.token = null;
  api.fetchMaster.mockResolvedValue({ areas: [], stations: [], occupations: [], employments: [] });
  api.fetchFacets.mockResolvedValue({ total: 2, areas: [], stations: [], occupations: [], employments: [] });
  api.fetchRecruits.mockResolvedValue({ total: 2, page: 1, per_page: 10, last_page: 1, items: [job(1), job(2)] });
  api.fetchRecommendations.mockResolvedValue([job(11), job(12)]);
});

describe('一覧（トップ）の「あなたへのおすすめ」', () => {
  it('ログイン中で、条件なしの 1 ページ目なら、一覧の上（しぼり込みと検索結果の間）に帯を出す。API へ JWT と件数 6 を渡す', async () => {
    session.token = 'jwt-123';
    const { container } = render(await Home(params()));
    expect(api.fetchRecommendations).toHaveBeenCalledWith('jwt-123', 6);
    const recs = band();
    expect(recs).not.toBeNull();
    expect([...recs!.querySelectorAll('h3')].map((h) => h.textContent)).toEqual(['職種 11', '職種 12']);
    // 並び: しぼり込み → おすすめの帯 → 検索結果。検索結果の一覧（h2 のカード）は、帯の影響を受けない
    const order = [...container.querySelectorAll('[data-testid="filter-form"], section.recs, section.results')].map((el) => el.tagName + '.' + el.className);
    expect(order).toEqual(['DIV.', 'SECTION.recs', 'SECTION.results']);
    const results = container.querySelector('section.results')!;
    expect([...results.querySelectorAll('article.card h2')].map((h) => h.textContent)).toEqual(['職種 1', '職種 2']);
    expect(results.querySelector('nav.pager')).not.toBeNull();
    expect(recs!.querySelector('.pager')).toBeNull();
  });

  it('未ログイン（JWT が取れない）なら、帯を出さず、おすすめの API も呼ばない（401 を出さない）。一覧はそのまま', async () => {
    const { container } = render(await Home(params()));
    expect(band()).toBeNull();
    expect(api.fetchRecommendations).not.toHaveBeenCalled();
    expect(container.querySelectorAll('section.results article.card')).toHaveLength(2);
  });

  it('履歴が無い（[]）なら、帯ごと出さない', async () => {
    session.token = 'jwt-123';
    api.fetchRecommendations.mockResolvedValue([]);
    const { container } = render(await Home(params()));
    expect(api.fetchRecommendations).toHaveBeenCalledOnce();
    expect(band()).toBeNull();
    expect(container.querySelector('.recs')).toBeNull();
  });

  it.each([
    ['エリアの条件', { area: '3' }],
    ['雇用形態の条件', { employment: '1' }],
    ['キーワード', { q: '温室' }],
    ['条件とページ', { area: '3', page: '1' }],
  ])('検索条件（%s）が付いているときは、帯を出さず、おすすめの API も呼ばない', async (_name, p) => {
    session.token = 'jwt-123';
    render(await Home(params(p)));
    expect(band()).toBeNull();
    expect(api.fetchRecommendations).not.toHaveBeenCalled();
  });

  it('2 ページ目以降は、帯を出さない（一覧の続きを邪魔しない）', async () => {
    session.token = 'jwt-123';
    api.fetchRecruits.mockResolvedValue({ total: 12, page: 2, per_page: 10, last_page: 2, items: [job(11)] });
    render(await Home(params({ page: '2' })));
    expect(band()).toBeNull();
    expect(api.fetchRecommendations).not.toHaveBeenCalled();
  });

  it('おすすめの API が失敗しても（401・5xx・接続できない）、一覧は壊れず、帯だけ出ない', async () => {
    session.token = 'jwt-123';
    for (const status of [401, 500, 0]) {
      api.fetchRecommendations.mockImplementationOnce(async () => {
        throw new ApiError(status, 'x');
      });
      const { container, unmount } = render(await Home(params()));
      expect(band()).toBeNull();
      expect(screen.queryByRole('alert')).toBeNull();
      expect(container.querySelectorAll('section.results article.card')).toHaveLength(2);
      unmount();
    }
  });

  it('一覧そのものが読めないときは、これまでどおり固定の文（おすすめの有無によらない）', async () => {
    session.token = 'jwt-123';
    api.fetchRecruits.mockRejectedValue(new ApiError(500, 'x'));
    render(await Home(params()));
    expect(screen.getByRole('alert')).toHaveTextContent('求人を読み込めませんでした');
    expect(band()).toBeNull();
  });
});

describe('一覧（トップ）の件数と条件バー', () => {
  it('条件が無いときは「全N件」、あるときは「検索結果 N件」（DocuNavi の資料一覧と同じ書き方。数字の前後に空白を入れない）', async () => {
    api.fetchRecruits.mockResolvedValue({ total: 26, page: 1, per_page: 10, last_page: 3, items: [job(1)] });
    const first = render(await Home(params()));
    expect(first.container.querySelector('.result-count')).toHaveTextContent(/^全26件$/);
    first.unmount();
    const second = render(await Home(params({ area: '3' })));
    expect(second.container.querySelector('.result-count')).toHaveTextContent(/^検索結果 26件$/);
  });

  it('件数は見出しの横（DocuNavi の資料一覧と同じ位置）。しぼり込みは、画面の上に留まる条件バー（.search-bar）の中', async () => {
    const { container } = render(await Home(params()));
    // 見出しの縦位置を DocuNavi にそろえる印（globals.css の .page.recruit-list）
    expect(container.querySelector('main')).toHaveClass('page', 'recruit-list');
    const head = container.querySelector('.title-row')!;
    expect(head.querySelector('h1')).not.toBeNull();
    expect(head.querySelector('.result-count')).not.toBeNull();
    const bar = container.querySelector('.search-bar')!;
    expect(bar.querySelector('[data-testid="filter-form"]')).not.toBeNull();
    // 見出し・件数・診断の案内は、固定しない（バーの外）
    expect(bar.querySelector('h1, .result-count, .diagnosis-hint')).toBeNull();
  });
});
