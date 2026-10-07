// freshgo: keys, and actions that do not leave the page. Every page works
// without this script; it only makes links and forms quicker to use.
(() => {
	'use strict';

	let config = {};
	try {
		config = JSON.parse(document.body.dataset.config || '{}');
	} catch {
		// A page without settings has no keys of its own.
	}
	const keys = config.keys || {};
	const urls = config.urls || {};
	const t = (key) => (config.texts && config.texts[key]) || key;

	const entries = document.querySelector('.entries');
	const tree = document.getElementById('tree');
	const messages = document.getElementById('messages');

	const el = (tag, attrs, ...children) => {
		const node = document.createElement(tag);
		for (const [name, value] of Object.entries(attrs || {})) {
			node.setAttribute(name, value);
		}
		node.append(...children);
		return node;
	};

	// say puts a message where screen readers announce it.
	const say = (text) => {
		if (messages) {
			messages.replaceChildren(el('p', null, text));
		}
	};

	const parse = (html) => new DOMParser().parseFromString(html, 'text/html');

	// ask fetches a page or a part of one as the reader in front of it.
	const ask = (url, options) => fetch(url, { credentials: 'same-origin', ...options });

	// ---- Entries ----

	// current is the entry keys act on, opened the one a key opened last.
	let current = null;
	let opened = null;

	const listed = () => (entries ? Array.from(entries.querySelectorAll('article.entry')) : []);
	const detailsOf = (article) => article.querySelector('details');
	const isOpen = (article) => detailsOf(article).open;

	const setCurrent = (article) => {
		if (current && current !== article) {
			current.classList.remove('current');
		}
		current = article;
		if (article) {
			article.classList.add('current');
		}
	};

	// wasOpened makes an entry read when the reader opens it, if they want so.
	const wasOpened = (article) => {
		if (config.markOnOpen && isOpen(article)) {
			act(article, 'read', true);
		}
	};

	const select = (article, open) => {
		if (open) {
			if (opened && opened !== article) {
				detailsOf(opened).open = false;
			}
			detailsOf(article).open = true;
			opened = article;
		}
		setCurrent(article);
		article.focus({ preventScroll: true });
		article.scrollIntoView({ block: 'start' });
		if (open) {
			wasOpened(article);
		}
	};

	// move goes to the next or the previous entry, loading the next page
	// when the list ends.
	const move = async (step, open, unreadOnly) => {
		const pick = () => {
			const list = listed();
			let at = current ? list.indexOf(current) : -1;
			if (at < 0 && step < 0) {
				return null;
			}
			for (at += step; at >= 0 && at < list.length; at += step) {
				if (!unreadOnly || !list[at].classList.contains('read')) {
					return list[at];
				}
			}
			return null;
		};
		let target = pick();
		while (!target && step > 0 && (await loadMore())) {
			target = pick();
		}
		if (target) {
			select(target, open);
		} else if (entries) {
			say(t('js.no-entry'));
		}
	};

	const toggle = (article) => {
		detailsOf(article).open = !isOpen(article);
		setCurrent(article);
		wasOpened(article);
	};

	// sync brings an entry of the page up to date with what the server
	// says it is now, without touching the text being read.
	const sync = (article, fresh) => {
		const focused = article.contains(document.activeElement) ? document.activeElement : null;
		const form = focused && focused.closest('.entry-actions form');
		const action = form && form.getAttribute('action');
		article.classList.toggle('read', fresh.classList.contains('read'));
		for (const part of ['.entry-state', '.entry-actions', '.entry-labels']) {
			const old = article.querySelector(part);
			const now = fresh.querySelector(part);
			if (old && now) {
				old.replaceWith(now);
			} else if (old) {
				old.remove();
			} else if (now) {
				article.querySelector('.entry-body').append(now);
			}
		}
		if (action) {
			// The button that was pressed is gone; its successor takes the focus.
			const button = article.querySelector(`.entry-actions form[action="${CSS.escape(action)}"] button`);
			(button || article).focus({ preventScroll: true });
		} else if (focused && !article.contains(document.activeElement)) {
			article.focus({ preventScroll: true });
		}
	};

	// Actions on entries go one after the other: each answer says what the
	// entry is like, and the last one asked for has to be the last one shown.
	let queue = Promise.resolve();
	const inTurn = (work) => {
		const turn = queue.then(work);
		queue = turn.catch(() => {});
		return turn;
	};

	// post submits a form about an entry and puts the answer in place.
	const post = async (form, article) => {
		let response;
		try {
			response = await ask(form.action, {
				method: 'POST',
				body: new URLSearchParams(new FormData(form)),
				headers: { 'X-Fragment': 'entry' },
			});
		} catch {
			say(t('js.failed'));
			return false;
		}
		const fresh = response.ok ? parse(await response.text()).querySelector('article.entry') : null;
		if (!fresh) {
			say(t('js.failed'));
			return false;
		}
		sync(article, fresh);
		const notice = response.headers.get('X-Notice');
		if (notice) {
			say(decodeURIComponent(notice));
		}
		refreshTree();
		return true;
	};

	// send submits a form about an entry when its turn comes. A form of the
	// entry that an answer has replaced meanwhile gives way to its successor.
	const send = (form, article) => inTurn(() => {
		const action = CSS.escape(form.getAttribute('action'));
		const live = form.isConnected ? form : article.querySelector(`.entry-actions form[action="${action}"]`);
		return live ? post(live, article) : false;
	});

	// act presses the button of an entry that marks it read or stars it.
	// With unreadOnly it leaves an entry alone that is read by the time its
	// turn comes: the button would make it unread again.
	const act = (article, kind, unreadOnly) => inTurn(() => {
		const form = article.querySelector(`.entry-actions form[action$="/${kind}"]`);
		if (!form || (unreadOnly && article.classList.contains('read'))) {
			return false;
		}
		return post(form, article);
	});

	if (entries) {
		entries.addEventListener('submit', (event) => {
			const article = event.target.closest('article.entry');
			if (article && event.target.matches('.entry-actions form')) {
				event.preventDefault();
				send(event.target, article);
			}
		});
		entries.addEventListener('focusin', (event) => {
			const article = event.target.closest('article.entry');
			if (article) {
				setCurrent(article);
			}
		});
		entries.addEventListener('click', (event) => {
			const summary = event.target.closest('summary');
			const article = summary && summary.closest('article.entry');
			if (article) {
				// The entry opens after this event.
				setTimeout(() => wasOpened(article));
			}
		});
		const named = location.hash.startsWith('#e') && document.getElementById(location.hash.slice(1));
		if (named && named.matches('article.entry')) {
			setCurrent(named);
			named.focus({ preventScroll: true });
		}
	}

	// ---- The next page ----

	let loading = null;

	// loadMore appends the next page to the list; false when there is none.
	const loadMore = () => {
		const link = document.querySelector('.more a[rel="next"]');
		if (!entries || !link) {
			return Promise.resolve(false);
		}
		if (!loading) {
			entries.setAttribute('aria-busy', 'true');
			loading = ask(link.href)
				.then((response) => (response.ok ? response.text() : Promise.reject(new Error(response.status))))
				.then((html) => {
					const page = parse(html);
					for (const article of page.querySelectorAll('.entries > article.entry')) {
						if (!document.getElementById(article.id)) {
							entries.append(document.adoptNode(article));
						}
					}
					const more = page.querySelector('.more');
					const old = document.querySelector('.more');
					if (more) {
						old.replaceWith(document.adoptNode(more));
						watchEnd();
					} else {
						old.remove();
					}
					return true;
				})
				.catch(() => {
					say(t('js.failed'));
					return false;
				})
				.finally(() => {
					entries.removeAttribute('aria-busy');
					loading = null;
				});
		}
		return loading;
	};

	const nearEnd = 'IntersectionObserver' in window && new IntersectionObserver((seen) => {
		if (seen.some((one) => one.isIntersecting)) {
			loadMore();
		}
	}, { rootMargin: '600px' });

	function watchEnd() {
		const more = document.querySelector('.more');
		if (config.autoLoad && nearEnd && more) {
			nearEnd.disconnect();
			nearEnd.observe(more);
		}
	}
	watchEnd();

	// ---- The tree ----

	let treeTimer = 0;

	// refreshTree brings the counts of the tree up to date.
	function refreshTree() {
		if (!tree) {
			return;
		}
		clearTimeout(treeTimer);
		treeTimer = setTimeout(async () => {
			let response;
			try {
				response = await ask(location.href, { headers: { 'X-Fragment': 'tree' } });
			} catch {
				return;
			}
			const fresh = response.ok ? parse(await response.text()).querySelector('details') : null;
			const old = tree.querySelector('details');
			if (!fresh || !old) {
				return;
			}
			const focused = tree.contains(document.activeElement) ? document.activeElement.getAttribute('href') : null;
			fresh.open = old.open;
			old.replaceWith(document.adoptNode(fresh));
			if (focused) {
				const link = Array.from(tree.querySelectorAll('a')).find((a) => a.getAttribute('href') === focused);
				(link || tree.querySelector('summary')).focus({ preventScroll: true });
			}
		}, 200);
	}

	if (tree) {
		// On a narrow screen the tree stands above the list: fold it.
		if (window.matchMedia('(max-width: 48rem)').matches) {
			tree.querySelector('details').open = false;
		}
		setInterval(() => {
			if (!document.hidden) {
				refreshTree();
			}
		}, 120000);
	}

	// goNode opens the next or the previous stream of the tree.
	const goNode = (step, unreadOnly) => {
		if (!tree) {
			return;
		}
		const links = Array.from(tree.querySelectorAll('li > a'));
		let at = links.findIndex((a) => a.getAttribute('aria-current') === 'page');
		for (at += step; at >= 0 && at < links.length; at += step) {
			if (!unreadOnly || links[at].parentElement.querySelector(':scope > .count')) {
				location.href = links[at].href;
				return;
			}
		}
	};

	// ---- Dialogs ----

	let dialogs = 0;

	// dialog shows a modal dialog; the browser keeps the focus inside it and
	// Escape closes it, and the focus goes back to where it was.
	const dialog = (title, ...content) => {
		const id = `dialog-${++dialogs}`;
		const before = document.activeElement;
		const close = el('button', { type: 'button', class: 'link dialog-close' }, t('js.close'));
		const box = el('dialog', { class: 'dialog', 'aria-labelledby': id },
			el('header', null, el('h2', { id }, title), close), ...content);
		close.addEventListener('click', () => box.close());
		box.addEventListener('close', () => {
			box.remove();
			if (before && before.isConnected) {
				before.focus({ preventScroll: true });
			}
		});
		document.body.append(box);
		box.showModal();
		return box;
	};

	const showHelp = () => {
		const row = (key, name) => el('tr', null, el('td', null, key), el('th', { scope: 'row' }, name));
		const body = el('tbody', null,
			row(el('kbd', null, 'Enter'), t('js.help.fixed-enter')),
			row(el('kbd', null, 'Escape'), t('js.help.fixed-escape')),
			row(el('kbd', null, 'Ctrl+k'), t('js.help.fixed-palette')));
		for (const action of config.actions || []) {
			const works = action.key && allowed(action.key);
			body.append(row(works ? el('kbd', null, action.key) : t('js.help.none'), action.name));
		}
		const table = el('table', null,
			el('thead', null, el('tr', null,
				el('th', { scope: 'col' }, t('js.help.key')), el('th', { scope: 'col' }, t('js.help.action')))),
			body);
		const box = dialog(t('js.help'), el('div', { class: 'dialog-body', tabindex: '0' }, table));
		box.querySelector('.dialog-body').focus();
	};

	const confirmMarkAll = () => {
		const form = document.querySelector('.menu form[action$="/read-all"]');
		if (!form) {
			return;
		}
		const yes = el('button', { type: 'button' }, t('js.confirm'));
		const no = el('button', { type: 'button', class: 'link' }, t('js.cancel'));
		const box = dialog(t('js.confirm'), el('p', null, t('js.mark-all')), el('p', { class: 'dialog-buttons' }, yes, no));
		no.addEventListener('click', () => box.close());
		yes.addEventListener('click', () => {
			box.close();
			form.requestSubmit(form.querySelector('button[name="older"][value=""]'));
		});
		yes.focus();
	};

	// showLabels offers the labels of an entry in a dialog: the form of the
	// page of the entry, sent from here.
	const showLabels = async (article) => {
		const page = article.querySelector('.entry-actions a.entry-page');
		let form = null;
		try {
			const response = await ask(page.href);
			form = response.ok ? parse(await response.text()).querySelector('form#labels') : null;
		} catch {
			// Said below.
		}
		if (!form) {
			say(t('js.failed'));
			return;
		}
		form = document.adoptNode(form);
		form.action = new URL(form.getAttribute('action'), page.href).href;
		const here = article.querySelector('.entry-actions input[name="next"]');
		if (here) {
			form.querySelector('input[name="next"]').value = here.value;
		}
		const box = dialog(t('js.labels'), form);
		form.addEventListener('submit', async (event) => {
			event.preventDefault();
			if (await send(form, article)) {
				box.close();
			}
		});
		(form.querySelector('input[type="checkbox"], input[type="text"]') || form).focus();
	};

	// ---- The command palette ----

	let places = null;

	// rank says how well a text answers what was typed: the characters in
	// order, the closer together and the nearer the start the better; -1
	// when they are not all there.
	const rank = (typed, text) => {
		const hay = text.toLocaleLowerCase();
		let score = 0;
		let from = 0;
		let last = -1;
		for (const letter of typed) {
			const at = hay.indexOf(letter, from);
			if (at < 0) {
				return -1;
			}
			score += at === last + 1 ? 3 : 1;
			if (at === 0 || /[\s\-_/.]/.test(hay[at - 1])) {
				score += 2;
			}
			last = at;
			from = at + 1;
		}
		return score - hay.length / 1000;
	};

	const showPalette = async () => {
		const input = el('input', {
			type: 'text', id: 'palette-input', role: 'combobox', autocomplete: 'off', spellcheck: 'false',
			'aria-expanded': 'true', 'aria-controls': 'palette-list', 'aria-autocomplete': 'list',
			'aria-describedby': 'palette-hint',
		});
		const list = el('ul', { id: 'palette-list', role: 'listbox', 'aria-label': t('js.palette') });
		const status = el('p', { class: 'muted', role: 'status' });
		const box = dialog(t('js.palette'),
			el('label', { for: 'palette-input', id: 'palette-hint' }, t('js.palette.hint')), input, list, status);
		input.focus();

		const commands = (config.actions || []).map((action) => ({
			name: action.name, kind: t('js.palette.action'), key: action.key && allowed(action.key) ? action.key : '',
			run: () => run(action.id),
		}));
		let offered = [];
		let active = 0;

		const mark = () => {
			list.querySelectorAll('li').forEach((item, i) => item.setAttribute('aria-selected', String(i === active)));
			const item = list.children[active];
			if (item) {
				input.setAttribute('aria-activedescendant', item.id);
				item.scrollIntoView({ block: 'nearest' });
			} else {
				input.removeAttribute('aria-activedescendant');
			}
		};
		const offer = () => {
			const typed = input.value.trim().toLocaleLowerCase();
			const all = commands.concat((places || []).map((place) => ({
				name: place.name, kind: place.kind, run: () => { location.href = place.url; },
			})));
			offered = all
				.map((item, order) => ({ item, order, score: typed ? rank(typed, item.name) : 0 }))
				.filter((one) => one.score >= 0)
				.sort((a, b) => b.score - a.score || a.order - b.order)
				.slice(0, 50)
				.map((one) => one.item);
			active = 0;
			list.replaceChildren(...offered.map((item, i) => {
				const option = el('li', { role: 'option', id: `palette-option-${i}` },
					el('span', { class: 'palette-name' }, item.name), el('span', { class: 'muted' }, item.kind));
				if (item.key) {
					option.append(el('kbd', null, item.key));
				}
				option.addEventListener('click', () => choose(i));
				return option;
			}));
			status.textContent = offered.length ? '' : t('js.palette.empty');
			mark();
		};
		const choose = (i) => {
			const item = offered[i];
			if (item) {
				box.close();
				item.run();
			}
		};
		input.addEventListener('input', offer);
		input.addEventListener('keydown', (event) => {
			if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
				event.preventDefault();
				if (offered.length) {
					active = (active + (event.key === 'ArrowDown' ? 1 : offered.length - 1)) % offered.length;
					mark();
				}
			} else if (event.key === 'Enter') {
				event.preventDefault();
				choose(active);
			}
		});
		offer();
		if (!places && urls.palette) {
			try {
				const response = await ask(urls.palette);
				places = response.ok ? await response.json() : [];
			} catch {
				places = [];
			}
			if (box.isConnected) {
				offer();
			}
		}
	};

	// ---- Keys ----

	// What the keys of a keyboard give on the US layout, for layouts whose
	// characters no key of freshgo is made of: plain and with Shift.
	const usLayout = {
		Slash: '/?', Semicolon: ';:', Comma: ',<', Period: '.>', Minus: '-_', Equal: '=+', BracketLeft: '[{',
		BracketRight: ']}', Quote: '\'"', Backquote: '`~', Backslash: '\\|',
		Digit0: '0)', Digit1: '1!', Digit2: '2@', Digit3: '3#', Digit4: '4$', Digit5: '5%', Digit6: '6^', Digit7: '7&',
		Digit8: '8*', Digit9: '9(',
	};

	// stroke names the key of an event the way bindings are spelled.
	const stroke = (event) => {
		let key = event.key;
		if (key === ' ') {
			key = 'Space';
		} else if (key.length === 1 && key.charCodeAt(0) > 127) {
			if (/^Key[A-Z]$/.test(event.code)) {
				key = event.shiftKey ? event.code[3] : event.code[3].toLowerCase();
			} else if (usLayout[event.code]) {
				key = usLayout[event.code][event.shiftKey ? 1 : 0];
			}
		}
		const modifiers = (event.ctrlKey ? 'Ctrl+' : '') + (event.altKey ? 'Alt+' : '') + (event.metaKey ? 'Meta+' : '');
		if (key.length === 1) {
			return modifiers + (modifiers ? key.toLowerCase() : key);
		}
		return modifiers + (event.shiftKey ? 'Shift+' : '') + key;
	};

	const isSingle = (one) => !/^(Ctrl|Alt|Meta)\+/.test(one) && (one.length === 1 || one === 'Space');

	// allowed is false for a binding of characters pressed alone when the
	// reader has turned those off.
	function allowed(binding) {
		return config.singleKeys || !binding.split(' ').every(isSingle);
	}

	const prefixes = new Set(Object.keys(keys).filter((binding) => binding.includes(' ')).map((binding) => binding.split(' ')[0]));
	let pending = '';
	let pendingTimer = 0;

	// run does an action by its name.
	function run(id) {
		const entry = () => current || listed()[0];
		switch (id) {
		case 'next': return move(1, true, false);
		case 'prev': return move(-1, true, false);
		case 'next-unread': return move(1, true, true);
		case 'skip-next': return move(1, false, false);
		case 'skip-prev': return move(-1, false, false);
		case 'toggle': return entry() && toggle(entry());
		case 'page': {
			if (current && isOpen(current) && current.getBoundingClientRect().bottom > window.innerHeight) {
				return window.scrollBy(0, window.innerHeight * 0.85);
			}
			return move(1, true, false);
		}
		case 'original': {
			const link = current && current.querySelector('.entry-actions a.original');
			return link && window.open(link.href, '_blank', 'noopener');
		}
		case 'read': return current && act(current, 'read');
		case 'star': return current && act(current, 'star');
		case 'labels': return current && current.querySelector('.entry-actions form') && showLabels(current);
		case 'mark-all': return confirmMarkAll();
		case 'more': return loadMore();
		case 'refresh': return location.reload();
		case 'search': {
			const search = document.getElementById('q');
			if (search) {
				search.focus();
				search.select();
			}
			return undefined;
		}
		case 'next-node': return goNode(1, false);
		case 'prev-node': return goNode(-1, false);
		case 'unread-node': return goNode(1, true);
		case 'tree': {
			const fold = tree && tree.querySelector('details');
			if (fold) {
				fold.open = !fold.open;
				fold.querySelector('summary').focus();
			}
			return undefined;
		}
		case 'go-unread': location.href = urls.unread; return undefined;
		case 'go-all': location.href = urls.all; return undefined;
		case 'go-starred': location.href = urls.starred; return undefined;
		case 'go-keys': location.href = urls.keys; return undefined;
		case 'palette': return showPalette();
		case 'help': return showHelp();
		default: return undefined;
		}
	}

	document.addEventListener('keydown', (event) => {
		// A modifier going down is part of the key that follows it.
		if (event.defaultPrevented || event.isComposing || ['Shift', 'Control', 'Alt', 'Meta', 'AltGraph', 'CapsLock'].includes(event.key)) {
			return;
		}
		const one = stroke(event);
		const inDialog = document.querySelector('dialog[open]');
		if (one === 'Ctrl+k' || one === 'Meta+k') {
			event.preventDefault();
			if (!inDialog) {
				showPalette();
			}
			return;
		}
		if (inDialog) {
			return;
		}
		const target = event.target;
		const typing = target.closest('input, textarea, select, [contenteditable]');
		if (event.key === 'Escape') {
			// Out of a field or a menu, back to the entry being read.
			const menu = target.closest('details.menu[open]');
			if (menu) {
				menu.open = false;
				menu.querySelector('summary').focus();
			} else if (typing) {
				(current || document.getElementById('content')).focus({ preventScroll: true });
			}
			return;
		}
		if (typing) {
			return;
		}
		if (event.key === 'Enter') {
			if (target.matches('article.entry')) {
				event.preventDefault();
				toggle(target);
			}
			return;
		}
		// Space presses the button, the link or the summary it is on.
		if (one === 'Space' && target.closest('button, a, summary')) {
			return;
		}
		const binding = pending ? `${pending} ${one}` : one;
		clearTimeout(pendingTimer);
		pending = '';
		const id = keys[binding];
		if (id && allowed(binding)) {
			event.preventDefault();
			run(id);
		} else if (!binding.includes(' ') && prefixes.has(one) && allowed(`${one} x`)) {
			event.preventDefault();
			pending = one;
			pendingTimer = setTimeout(() => { pending = ''; }, 1500);
		}
	});
})();
