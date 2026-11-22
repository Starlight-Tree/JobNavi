import math
from typing import Annotated

from fastapi import APIRouter, HTTPException, Query

from app.db import connection
from app.image_queries import hero_images, public_images
from app.schemas import Facets, Master, RecruitDetail, RecruitPage, RecruitSummary
from app.storage import get_storage

router = APIRouter(prefix="/api")

# 表示用の名前は、検索用ビュー（recruit_search）の配列の番号から、マスタの名前を引いて付ける
_NAMES = """
  ARRAY(SELECT a.name FROM areas a WHERE a.code = ANY(s.area_codes) ORDER BY a.sort) AS areas,
  ARRAY(SELECT t.name FROM stations t WHERE t.code = ANY(s.station_codes) ORDER BY t.sort) AS stations,
  ARRAY(SELECT o.name FROM occupations o WHERE o.code = ANY(s.occupation_codes) ORDER BY o.sort) AS occupations,
  ARRAY(SELECT e.name FROM employments e WHERE e.code = ANY(s.employment_codes) ORDER BY e.sort) AS employments
"""

_SUMMARY_COLS = "s.id, s.corporate, s.category, s.salary, s.pay_min, s.pay_max, s.comment1, s.note1, s.formfile"

# 条件の種類: (応答での名前, 検索用ビューの配列の列, クエリの名前)
_KINDS = (
    ("areas", "area_codes", "area"),
    ("stations", "station_codes", "station"),
    ("occupations", "occupation_codes", "occupation"),
    ("employments", "employment_codes", "employment"),
)

# キーワード（q）。空白（全角も）で区切った語が、すべて、検索用の文字列（recruit_search.search_text）に含まれる求人だけ（語の間は AND）。
# search_text は、職種名・法人名・紹介・所在地・業務内容 1〜3 をつなげて、正規化したもの（0009 の recruit_search_text。対象の列を変えるときは、そこを直す）。
# 備考（note1〜3）は、「交通費は規定により支給」のような定型文が多く、ノイズになるので対象にしない。
# 正規化（search_fold）は、NFKC → 小文字 → ひらがな→カタカナ。求人の側（保存した列）と検索語の側（search_pattern）で、同じ DB の関数を通す（規則を 1 か所にするため）。
# 半角カナ・全角英数字・大文字小文字・ひらがな/カタカナの違いを吸収する。漢字の読み（おんしつ → 温室）は、辞書が要るので対象外。
KEYWORD_MAX_LENGTH = 100
KEYWORD_MAX_TERMS = 5

AreaQ = Annotated[list[int] | None, Query()]
StationQ = Annotated[list[int] | None, Query()]
OccupationQ = Annotated[list[int] | None, Query()]
EmploymentQ = Annotated[list[int] | None, Query()]
KeywordQ = Annotated[str | None, Query(max_length=KEYWORD_MAX_LENGTH)]


def _keyword_terms(q: str | None) -> list[str]:
    """キーワードを、語に分ける。空白（全角を含む）で区切り、重複を除き、先頭から KEYWORD_MAX_TERMS 語まで。空なら []。"""
    return list(dict.fromkeys((q or "").split()))[:KEYWORD_MAX_TERMS]


def _where(selected: dict[str, list[int] | None], skip: str | None = None, q: str | None = None) -> tuple[str, dict[str, object]]:
    """検索の規則: 同じ種類の条件の中は OR（配列が重なれば一致）、種類の間は AND。キーワード（q）も AND。表示中の求人だけ。

    skip に種類の名前を渡すと、その種類の条件は使わない（件数の数え方に使う。キーワードは、いつも使う）。
    """
    where = ["s.is_displayed"]
    params: dict[str, object] = {}
    for name, column, key in _KINDS:
        values = selected.get(key)
        if values and name != skip:
            where.append(f"s.{column} && %({key})s::int[]")
            params[key] = values
    for i, term in enumerate(_keyword_terms(q)):
        # 語は、生のまま渡す。DB の search_pattern が、正規化してから % _ \\ をエスケープして（ワイルドカードにせず）、%語% にする
        where.append(f"s.search_text LIKE search_pattern(%(kw{i})s::text)")
        params[f"kw{i}"] = term
    return " AND ".join(where), params


@router.get("/master", response_model=Master)
def master() -> Master:
    with connection() as conn:
        def codes(table: str) -> list[dict]:
            return conn.execute(
                f"SELECT code, name FROM {table} WHERE NOT is_deleted ORDER BY sort"  # noqa: S608 テーブル名は固定の文字列
            ).fetchall()

        occupations = conn.execute(
            "SELECT code, category, name FROM occupations WHERE NOT is_deleted ORDER BY sort"
        ).fetchall()
        groups: list[dict] = []
        for row in occupations:
            if not groups or groups[-1]["category"] != row["category"]:
                groups.append({"category": row["category"], "items": []})
            groups[-1]["items"].append({"code": row["code"], "name": row["name"]})
        return Master(
            areas=codes("areas"), stations=codes("stations"), occupations=groups, employments=codes("employments")
        )


@router.get("/facets", response_model=Facets)
def facets(
    area: AreaQ = None, station: StationQ = None, occupation: OccupationQ = None, employment: EmploymentQ = None, q: KeywordQ = None
) -> Facets:
    selected = {"area": area, "station": station, "occupation": occupation, "employment": employment}
    clause, params = _where(selected, q=q)
    counts: dict[str, list[dict]] = {}
    with connection() as conn:
        total = conn.execute(f"SELECT count(*) AS n FROM recruit_search s WHERE {clause}", params).fetchone()["n"]  # noqa: S608
        for name, column, _ in _KINDS:
            others, other_params = _where(selected, skip=name, q=q)
            counts[name] = conn.execute(
                f"SELECT c AS code, count(*)::int AS count FROM recruit_search s, unnest(s.{column}) AS c "  # noqa: S608
                f"WHERE {others} GROUP BY c ORDER BY c",
                other_params,
            ).fetchall()
    return Facets(total=total, **counts)


@router.get("/recruits", response_model=RecruitPage)
def search_recruits(
    area: AreaQ = None,
    station: StationQ = None,
    occupation: OccupationQ = None,
    employment: EmploymentQ = None,
    q: KeywordQ = None,
    page: Annotated[int, Query(ge=1)] = 1,
    per_page: Annotated[int, Query(ge=1, le=50)] = 10,
) -> RecruitPage:
    clause, params = _where({"area": area, "station": station, "occupation": occupation, "employment": employment}, q=q)
    with connection() as conn:
        total = conn.execute(f"SELECT count(*) AS n FROM recruit_search s WHERE {clause}", params).fetchone()["n"]  # noqa: S608
        rows = conn.execute(
            f"SELECT {_SUMMARY_COLS}, {_NAMES} FROM recruit_search s WHERE {clause} "  # noqa: S608
            "ORDER BY s.id LIMIT %(limit)s OFFSET %(offset)s",
            {**params, "limit": per_page, "offset": (page - 1) * per_page},
        ).fetchall()
        heroes = hero_images(conn, [row["id"] for row in rows], get_storage())  # 画像は、この 1 ページの分をまとめて 1 回で引く
    return RecruitPage(
        total=total,
        page=page,
        per_page=per_page,
        last_page=max(1, math.ceil(total / per_page)),
        items=[RecruitSummary(**row, hero_image=heroes.get(row["id"])) for row in rows],
    )


@router.get("/recruits/{recruit_id}", response_model=RecruitDetail)
def recruit_detail(recruit_id: int) -> RecruitDetail:
    with connection() as conn:
        row = conn.execute(
            f"SELECT {_SUMMARY_COLS}, s.profile, s.place, s.note2, s.note3, s.comment2, s.comment3, "  # noqa: S608
            "s.workday, s.insurance, "
            "NULLIF(concat_ws(':', s.start_hour, s.start_minute), '') AS start_time, "
            "NULLIF(concat_ws(':', s.end_hour, s.end_minute), '') AS end_time, "
            f"{_NAMES} FROM recruit_search s WHERE s.id = %(id)s AND s.is_displayed",
            {"id": recruit_id},
        ).fetchone()
        if row is None:
            raise HTTPException(status_code=404, detail="求人が見つかりません")
        images = public_images(conn, recruit_id, get_storage())
    hero = next((i for i, is_hero in images if is_hero), images[0][0] if images else None)
    return RecruitDetail(**row, hero_image=hero, images=[i for i, _ in images])
