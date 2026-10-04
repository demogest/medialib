// The first-run guide lives on Home now (it is what Home shows until a library exists); old links still land there.
import { replace } from '../lib/router.js';

export async function mount() {
  replace('home');
  return {};
}
