// 入力の検査。最終の判断は API（backend/body.go）がする。ここは、送る前に分かる誤りを、画面で先に知らせるためのもの。

export type Field = 'display_name' | 'email' | 'password';
export type FieldErrors = Partial<Record<Field, string>>;

const EMAIL = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

// API は文字数を「文字」で数える。String.length は UTF-16 の単位なので、コードポイントで数える
const count = (s: string) => [...s].length;

function email(value: string): string | undefined {
  if (value === '') return 'メールアドレスを入力してください';
  if (!EMAIL.test(value)) return 'メールアドレスの形式が正しくありません（例: name@example.com）';
  return undefined;
}

/** 表示名の上限（文字数）。API（backend/body.go）と同じ。 */
export const DISPLAY_NAME_MAX = 30;

function displayName(value: string): string | undefined {
  if (value === '') return '表示名を入力してください';
  if (/\p{Cc}/u.test(value)) return '表示名に使えない文字（改行・タブなど）が含まれています';
  if (count(value) > DISPLAY_NAME_MAX) return `表示名は ${DISPLAY_NAME_MAX} 文字以内で入力してください`;
  return undefined;
}

function compact(errors: FieldErrors): FieldErrors {
  return Object.fromEntries(Object.entries(errors).filter(([, v]) => v !== undefined)) as FieldErrors;
}

/** 表示名（ニックネーム）の変更。前後の空白を除いた 1〜30 文字で、改行・タブなどを含まない。 */
export function validateName(input: { display_name: string }): FieldErrors {
  return compact({ display_name: displayName(input.display_name) });
}

export function validateLogin(input: { email: string; password: string }): FieldErrors {
  return compact({
    email: email(input.email),
    // ログインでは、パスワードの長さの決まりは見せない（空と上限だけ）
    password:
      input.password === ''
        ? 'パスワードを入力してください'
        : count(input.password) > 128
          ? 'パスワードは 128 文字以内で入力してください'
          : undefined,
  });
}
