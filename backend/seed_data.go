package main

// ダミーデータ（架空の宇宙の植物園・ガーデンの法人と利用者）。backend/app/seed_demo.py のデータから生成した（転記の誤りを避けるため）。実在の人・法人のデータは使わない。
// ログイン確認用: 利用者 hoshiniwa-user@example.com / 園 hoshiniwa-garden@example.com と hoshiniwa-garden2@example.com。パスワードはどれも demoPassword（ローカル専用）。

const demoPassword = "star-garden-demo"
const payNote = "交通費は規定により支給します。" // recruits.note1（詳細の「給与の備考」）

// 給与は架空の通貨「ルミ」（常勤は月給制、それ以外は時給制）。
type seedRecruit struct {
	DemoKey, Corporate, Category string
	Employ, PayMin, PayMax       int
	Areas, Stations, Occupations []int
	Shown                        bool
}

var seedRecruits = []seedRecruit{
	{"greenhouse-vine-grower", "環軌道植物公社", "無重力蔓の育成士", 1, 180000, 240000, []int{3}, []int{5}, []int{1}, true},
	{"artificial-night-keeper", "環軌道植物公社", "人工夜の温室管理員", 1, 190000, 250000, []int{4}, []int{2}, []int{2}, true},
	{"garden-storyteller", "ミルフェン月面植物園", "植物園の語り部", 2, 1100, 1300, []int{8}, []int{7}, []int{9}, true},
	{"night-blossom-guide", "ミルフェン月面植物園", "夜の花見ガイド", 1, 200000, 260000, []int{9, 10}, []int{7, 9}, []int{11}, true},
	{"oxygen-unit-maintainer", "ザーハード砂漠温室組合", "酸素供給装置の保守員", 1, 210000, 270000, []int{12}, []int{4, 11}, []int{6}, true},
	{"dune-vehicle-guide", "ザーハード砂漠温室組合", "砂丘の高機動車の運転案内", 3, 1000, 1200, []int{12}, []int{4, 11}, []int{12}, true},
	{"ruins-storyteller", "遺跡前ガーデン", "遺跡前の語り部", 2, 1150, 1350, []int{13, 14}, []int{10, 11}, []int{10}, true},
	{"ruins-soil-surveyor", "遺跡前ガーデン", "遺跡周辺の土壌の調査員", 4, 1500, 1700, []int{14}, []int{10}, []int{19}, true},
	{"gravity-crossbreeding-coordinator", "星庭財団", "重力別の交配調整員", 3, 1300, 1500, []int{3, 4}, []int{5, 6}, []int{17}, true},
	{"seed-recorder", "星庭財団", "種子の記録係", 1, 185000, 230000, []int{4}, []int{1}, []int{16}, true},
	{"plant-quarantine-officer", "静止軌道ガーデンズ", "持ち込み植物の検疫官", 2, 1100, 1250, []int{6}, []int{1, 6}, []int{25}, true},
	{"hidden-test-lead", "非表示のテスト求人", "栽培の統括", 1, 100000, 100000, []int{1}, []int{1}, []int{7}, false},
}

// --extend で足す求人（形は seedRecruits と同じ）。
var seedExtraRecruits = []seedRecruit{
	{"plant-doctor", "静止軌道ガーデンズ", "植物医", 1, 220000, 280000, []int{6}, []int{1, 6}, []int{23}, true},
	{"garden-pr-officer", "ミルフェン月面植物園", "広報担当", 1, 190000, 240000, []int{9}, []int{7}, []int{33}, true},
	{"entrance-receptionist", "根元迎賓ガーデン", "入園受付", 2, 1050, 1200, []int{6}, []int{6}, []int{30}, true},
	{"water-cycle-technician", "南縁ドーム管理局", "水循環の技師", 1, 200000, 260000, []int{10}, []int{9}, []int{27}, true},
	{"dome-glass-cleaner", "南縁ドーム管理局", "ドームのガラス清掃員", 3, 1000, 1250, []int{10}, []int{9}, []int{35}, true},
	{"seedling-courier", "北縁種苗センター", "苗の搬送員", 2, 1100, 1300, []int{8}, []int{7, 9}, []int{4}, true},
	{"fungi-cultivator", "岩山菌類園", "キノコの栽培員", 1, 195000, 250000, []int{13}, []int{10}, []int{8}, true},
	{"pollinator-keeper", "霧樹の森ガーデン", "受粉昆虫の飼育員", 1, 190000, 240000, []int{3}, []int{5}, []int{8}, true},
	{"flower-workshop-instructor", "ノクティラ環 花の学校", "花束体験教室の講師", 2, 1200, 1400, []int{2}, []int{2}, []int{13}, true},
	{"mist-tree-pruner", "霧樹の森ガーデン", "霧樹の剪定士", 1, 200000, 260000, []int{3}, []int{5}, []int{3}, true},
	{"hydroponic-shelf-manager", "北縁種苗センター", "水耕棚の管理員", 1, 185000, 235000, []int{8}, []int{7}, []int{5}, true},
	{"soil-nutrient-analyst", "岩山菌類園", "土壌と培養液の分析員", 1, 210000, 270000, []int{13}, []int{10}, []int{18}, true},
	{"rare-species-conservator", "星庭財団", "希少種の保全担当", 1, 220000, 280000, []int{4}, []int{1}, []int{20}, true},
	{"guide-desk-staff", "根元迎賓ガーデン", "案内所の係", 3, 1000, 1200, []int{6}, []int{6}, []int{14}, true},
	{"seasonal-event-planner", "ノクティラ環 花の学校", "季節の企画担当", 1, 190000, 250000, []int{2}, []int{2}, []int{31}, true},
	{"hidden-gas-detector", "遺跡前ガーデン", "有害ガスの検知員", 1, 200000, 250000, []int{14}, []int{10}, []int{26}, false},
	{"hidden-ticket-accountant", "ミルフェン月面植物園", "チケットと会計", 2, 1100, 1250, []int{9}, []int{7}, []int{32}, false},
	{"hidden-research-assistant", "南縁ドーム管理局", "研究の補助員", 2, 1050, 1200, []int{10}, []int{9}, []int{21}, false},
}

// 園のアカウント（メール, 法人名）。求人は、法人名が同じ園のアカウントに紐づける。
var seedGardens = []struct{ Email, Corporate string }{
	{"hoshiniwa-garden@example.com", "環軌道植物公社"},
	{"hoshiniwa-garden2@example.com", "ミルフェン月面植物園"},
}

// 「非表示のテスト求人」は、1 つ目の園の非表示の求人として使う。
var seedGardenOfCorporate = map[string]int{
	"環軌道植物公社":    0,
	"非表示のテスト求人":  0,
	"ミルフェン月面植物園": 1,
}

// 勤務時間と曜日（開始の時・分、終了の時・分、曜日）。seedRecruits は常に defaultSchedule。seedExtraRecruits は雇用形態で変える。
type seedSchedule struct{ StartHour, StartMinute, EndHour, EndMinute, Workday string }

var defaultSchedule = seedSchedule{"09", "00", "18", "00", "月〜金"}

var scheduleOfEmploy = map[int]seedSchedule{
	1: {"09", "00", "18", "00", "月〜金"},
	2: {"10", "00", "15", "00", "週3日〜（相談可）"},
	3: {"08", "00", "17", "00", "シフト制（土日を含む）"},
}

// 一覧に出す「業務内容」（recruits.comment1）。ここに無い demo_key は、職種名だけの短い文を使う。
var seedComments = map[string]string{
	"greenhouse-vine-grower":            "無重力の温室で、蔓の苗の剪定と誘引、伸び方の記録をお任せします。蔓は光の向きに合わせて育つため、毎日の観察が大切な仕事です。",
	"artificial-night-keeper":           "人工の夜をつくる温室で、照明と温度の切り替えを見守ります。植物の眠りと目覚めの時刻を整え、夜の開花を支える仕事です。",
	"garden-storyteller":                "来園者に、植物と園の歴史をお話しする仕事です。台本はありますが、お客様の反応に合わせて話し方を工夫できます。短い時間から働けます。",
	"night-blossom-guide":               "夜にだけ咲く花の見どころへ来園者を案内し、観賞の時間と安全を見守ります。花の種類や咲く時刻について、先輩が一から教えます。",
	"oxygen-unit-maintainer":            "温室の酸素供給装置を点検し、フィルターの交換と不具合の一次対応を行います。数値の記録と報告も業務に含まれます。",
	"dune-vehicle-guide":                "砂丘を走る高機動車で、来園者を温室群へ案内します。運転と車内でのご案内をお願いします。シフト制で、土日の勤務もあります。",
	"ruins-storyteller":                 "遺跡の前庭で、遺跡と植物のかかわりを来園者にお話しします。現地の調査員から聞いた話も交えて、わかりやすく伝える仕事です。",
	"ruins-soil-surveyor":               "遺跡の周辺で土を採取し、成分と湿り気を調べて記録します。屋外での作業があり、調査の結果は園の栽培計画に使われます。",
	"gravity-crossbreeding-coordinator": "重力の違う区画の間で、交配の日程と花粉の受け渡しを調整します。区画ごとの担当者と連絡を取り合う、段取りの仕事です。",
	"seed-recorder":                     "集めた種子の採取日・産地・保管場所を記録し、台帳を整えます。正確さが求められる、落ち着いた事務の仕事です。",
	"plant-quarantine-officer":          "ステーションへ持ち込まれる植物を調べ、害虫や病気の有無を確かめて、通してよいかを判断します。園の植物を守る、最初の砦の仕事です。",
	"hidden-test-lead":                  "栽培チームをまとめ、作業の計画と引き継ぎを管理します。メンバーの相談に乗り、育成の方針を決める立場です。",
	"plant-doctor":                      "弱った植物の症状を見立て、治療の方針を決めて、園の栽培員に伝えます。原因を突き止めるまでの観察と記録が仕事の中心です。",
	"garden-pr-officer":                 "園の魅力を、掲示や案内資料、来園者向けの知らせにして発信します。季節の見どころを取材し、わかりやすい言葉にまとめます。",
	"entrance-receptionist":             "入園の受付で、来園者の案内と入園票の確認を行います。はじめての方に園内の回り方をお伝えする、笑顔が大切な仕事です。",
	"water-cycle-technician":            "ドーム内の水の循環装置を点検し、水質の記録と部品の交換を行います。止まると園全体に響くため、日々の点検が欠かせません。",
	"dome-glass-cleaner":                "ドームの外壁ガラスを清掃し、日差しを妨げる汚れや傷の有無を確認します。高所での作業があり、安全の研修を受けてから始めます。",
	"seedling-courier":                  "育てた苗を、園や温室へ運びます。運搬中の温度と湿り気にも気を配る、丁寧さが求められる仕事です。短い時間から働けます。",
	"fungi-cultivator":                  "キノコの菌床を用意し、湿度と温度を見ながら栽培と収穫を行います。暗く涼しい培養室での作業が中心です。",
	"pollinator-keeper":                 "受粉を担う昆虫を飼育し、温室へ放す時期と数を調整します。生き物の世話が好きな方に向いています。",
	"flower-workshop-instructor":        "花束づくり体験の教室で、参加者に花の選び方と束ね方をお教えします。人に教えることが好きな方、短い時間から働きたい方を歓迎します。",
	"mist-tree-pruner":                  "霧樹の森で、枝の込み具合を見ながら剪定します。高所での作業があり、道具の扱いは入社後に研修でお伝えします。",
	"hydroponic-shelf-manager":          "水耕栽培の棚で、培養液の濃度を管理し、苗の育ちを記録します。毎日の変化に気づける、細やかな方に向いています。",
	"soil-nutrient-analyst":             "土壌と培養液の成分を分析し、結果を栽培員へ報告します。数値から育ち方を読み解く、研究に近い仕事です。",
	"rare-species-conservator":          "希少な植物の保全計画を立て、保存株の状態を定期的に点検します。種を絶やさないための、長い目で取り組む仕事です。",
	"guide-desk-staff":                  "案内所で、来園者の道案内と問い合わせへの対応をします。シフト制で、土日の勤務もあります。接客が好きな方を歓迎します。",
	"seasonal-event-planner":            "季節ごとの催しを企画し、会場づくりと運営の段取りを担当します。催しの当日は、現場で来園者を迎える役割も担います。",
	"hidden-gas-detector":               "遺跡前の園で、有害なガスの発生を検知し、安全を確かめて報告します。異常の早期発見が、来園者と職員を守ります。",
	"hidden-ticket-accountant":          "入園券の売上を集計し、日々の会計を記録します。数字を正確に扱う、落ち着いた事務の仕事です。",
	"hidden-research-assistant":         "研究チームの観察と測定を補助し、記録を整理します。研究の現場を支える、補助的な役割です。",
}
