// バックエンド（backend/recruits.go・auth.go・images_api.go の構造体）の応答の型
export type Code = { code: number; name: string };
export type OccupationGroup = { category: string; items: Code[] };
export type Master = {
  areas: Code[];
  stations: Code[];
  occupations: OccupationGroup[];
  employments: Code[];
};

// 公開してよい（承認済みの）画像。url は API が保存キーから組み立てた公開 URL
export type ImageInfo = { id: string; url: string; alt: string; width: number; height: number; mime_type: string };

export type RecruitSummary = {
  id: number;
  corporate: string | null;
  category: string | null;
  salary: number | null; // 1 年俸制 / 2 月給制 / 3 日給制 / 4 時給制
  pay_min: number | null;
  pay_max: number | null;
  comment1: string | null;
  note1: string | null;
  formfile: string | null;
  areas: string[];
  stations: string[];
  occupations: string[];
  employments: string[];
  hero_image: ImageInfo | null; // 一覧のサムネに使う代表画像。承認済みの画像が無ければ null
};

export type RecruitDetail = RecruitSummary & {
  images: ImageInfo[];
  profile: string | null;
  place: string | null;
  note2: string | null;
  note3: string | null;
  comment2: string | null;
  comment3: string | null;
  workday: string | null;
  insurance: string | null;
  start_time: string | null;
  end_time: string | null;
};

export type RecruitPage = {
  total: number;
  page: number;
  per_page: number;
  last_page: number;
  items: RecruitSummary[];
};

export type FacetCount = { code: number; count: number };
export type Facets = {
  total: number;
  areas: FacetCount[];
  stations: FacetCount[];
  occupations: FacetCount[];
  employments: FacetCount[];
};

export type User = { id: number; email: string; display_name: string | null; avatar_url?: string | null };

export type Garden = { id: number; email: string; corporate: string };
export type GardenSession = { token: string; garden: Garden };
export type GardenSummary = { corporate: string; email: string; displayed: number; hidden: number };

// 園の画像管理。status は approved（公開中）/ rejected（運営が取り下げた）。保存した画像は、承認を待たずに公開される
export type GardenImage = {
  id: string;
  alt: string;
  width: number;
  height: number;
  mime_type: string;
  origin: string;
  status: 'approved' | 'rejected';
  position: number;
  is_hero: boolean;
  url: string | null; // 公開中のときだけ公開 URL
};
export type GardenRecruitImages = { recruit_id: number; category: string | null; is_displayed: boolean; max_images: number; images: GardenImage[] };
export type GardenRecruitItem = { id: number; category: string | null; is_displayed: boolean; image_count: number; published_count: number };
