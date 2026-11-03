import type { AudibleAccount, AudibleAccounts, AudibleMapping, AudiblePolicy } from '@/api/types';

// The Audible storefronts, as the page names them (audible.MarketplaceCodes).
export const MARKETPLACE_NAMES: Record<string, string> = {
  us: 'United States',
  uk: 'United Kingdom',
  ca: 'Canada',
  au: 'Australia',
  de: 'Germany',
  fr: 'France',
  es: 'Spain',
  it: 'Italy',
  jp: 'Japan',
  in: 'India',
  br: 'Brazil',
};

export function marketplaceName(code: string): string {
  return MARKETPLACE_NAMES[code] ?? code.toUpperCase();
}

// What each policy keeps. The Bridge records it now and acts on it once it
// downloads books (a later version), so the words say what it is for.
export const POLICY_LABELS: Record<AudiblePolicy, string> = {
  all: 'Every new title',
  series: 'New titles in a series they already have',
  manual: 'Only the titles picked in the console',
};

// The marker the page Amazon lands on carries (audible.ParseAuthCode).
const CODE_PARAM = 'openid.oa2.authorization_code';

// redirectProblem says what is wrong with a pasted address, or null when it
// holds a sign-in code.
export function redirectProblem(text: string): string | null {
  const v = text.trim();
  if (v === '') return 'Paste the address of the page Amazon showed after you signed in.';
  let url: URL;
  try {
    url = new URL(v);
  } catch {
    return "That isn't a web address: copy the whole address from the browser's address bar.";
  }
  if (!url.searchParams.get(CODE_PARAM)) {
    return 'That address has no sign-in code: copy the address of the page Amazon showed after you signed in (it starts with the Amazon site and holds "authorization_code").';
  }
  return null;
}

// mappingProblem says what the mapping still needs, or null.
export function mappingProblem(m: Pick<AudibleMapping, 'user_id' | 'library_id'>): string | null {
  if (!m.user_id) return 'Pick the person the account belongs to.';
  if (!m.library_id) return 'Pick the books library its titles go to.';
  return null;
}

// defaultMapping is a new account's mapping: the only person and the only
// library when there is one of each, the manual policy, places synced.
export function defaultMapping(data: Pick<AudibleAccounts, 'users' | 'libraries'>): AudibleMapping {
  return {
    label: '',
    user_id: data.users.length === 1 ? data.users[0].id : 0,
    library_id: data.libraries.length === 1 ? data.libraries[0].id : 0,
    policy: 'manual',
    sync: true,
  };
}

// mappingOf is an account's mapping as the form edits it.
export function mappingOf(a: AudibleAccount): AudibleMapping {
  return {
    label: a.label,
    user_id: a.user_id,
    library_id: a.library_id,
    policy: a.policy,
    sync: a.sync,
  };
}

export type AudibleState = 'refused' | 'other_server' | 'checked' | 'waiting';

// accountState is what an account's badge says.
export function accountState(a: AudibleAccount): AudibleState {
  if (a.refused) return 'refused';
  if (a.other_server) return 'other_server';
  return a.last_poll_at ? 'checked' : 'waiting';
}

// pollSummary words the last complete poll ("" before the first).
export function pollSummary(a: AudibleAccount): string {
  if (!a.last_poll_at) return '';
  const titles = a.last_titles === 1 ? '1 title' : `${a.last_titles} titles`;
  return `${titles}, ${a.last_matched} already in the library`;
}
