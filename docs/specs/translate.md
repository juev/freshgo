# Translating entries

Status: implemented.
Sources: user requests of 2026-10-08 and 2026-10-09 (issue #31) and the measurements recorded there.

## Purpose and scope

A reader has the text of an entry translated into their language by a language model behind an API that speaks the chat completions of OpenAI. `internal/translate` cuts the text, asks the model and keeps the translation with the entry; `internal/web` offers it (`web.md`, U98). No service is built in.

## Requirements

- T1. The server is given the service by three settings: `-translate-url` (the address of the API without `/chat/completions`, `http` or `https`), `-translate-key` and `-translate-model`. All three or none: with some of them the server does not start. The help of a command does not show the key. Without them nothing is translated and nothing offers to.
- T2. A request to the service is a POST of the model and two messages, the instruction and the text, with the key as a bearer token, made by the client of freshgo for requests (its address rules, its proxy, its pauses) with three minutes for an answer and no redirects. Nothing else is sent: the model answers as the service has it by default.
- T3. The text of an entry is cut into paragraphs: runs of text with the inline elements inside them, ended by any other element and by a line break. Inside a paragraph an element is replaced by a numbered mark: `<1>…</1>` around its content, or `<2/>` when the element goes whole — code, and an element without two letters in a row, such as an image or the number of a footnote. A paragraph without two letters in a row is not sent, and nothing inside `pre` is. The model gets no tag, no attribute and no code.
- T4. Paragraphs are asked for together, up to 40 or about 3000 bytes a request — about 800 bytes in the first request for a text, whose answer is what a reader waits for before anything is translated — each on a line under its number in double square brackets; the answer is read by these numbers and taken paragraph by paragraph.
- T5. The answer to a paragraph is put in its place when every mark of the paragraph is there exactly once, pairs are closed in order, a whole element is not opened as a pair nor a pair made whole, and the text is not empty; the marks may have changed places. The elements come back with the attributes and, for whole ones, the content they had. A paragraph without a usable answer, and one of 25 characters or more given back as it was, is asked for again alone. What comes then is taken by the same rule, and a text given back as it was is taken for one that needs no translating; a paragraph still without a usable answer stays as it was and is counted as failed. A request that fails — the service refuses, does not answer in time, answers with what is no answer — is not such a paragraph: it is an error, and nothing of the request under way is kept.
- T6. An entry is translated in steps (`Translator.Step`): each step asks for the next request of paragraphs and stores the result; a step that fails with an error stores nothing, and the next one asks for the same paragraphs. Where a step goes on is kept as the number of runs of the text behind it, with words or without: how many runs a text has does not depend on what they say, so a translation as short as one letter or a number moves nothing. The first step asks the model two things at the same time, before its paragraphs: with the beginning of the text, whether it is in the target language already — then nothing is stored and the step says so — and for the translation of the title.
- T7. A translation is kept in the attributes of the entry under `translations`, by language tag: the title, the text as far as it is translated, how many paragraphs of how many were asked for, how many failed, whether the reader looks at the original, and a hash of the title and the text it was made from. The entry itself keeps its title and its text.
- T8. A translation whose hash is not that of the entry as it is does not exist: the entry is shown as it is and offers to be translated. So a change of the entry by its feed and a change of its text (`fulltext.md`, T12, T13) drop it. Translations of a text the entry no longer has are removed when one is stored.
- T9. `Translator.Show` switches between the translation and the original without a request; a step shows the translation again.
- T10. The language is told to the model by its English name.

## Decisions

- **The model gets paragraphs with marks, not markup and not pieces of text.** Two other ways were measured first. Handing over the HTML in parts of 6 KB let the model change elements and attributes, and one slip lost the part. Handing over the text nodes as a JSON array cut sentences at every link ("(formerly", "DMOZ", ")"), demanded as many strings back in the same order, which translating contradicts, and made the model escape quotes; a model gave up 19 parts of 76 that way. With paragraphs and marks, two models translated 1425 paragraphs of twelve entries with none and two given up and the markup of every entry kept.
- **No field that switches reasoning off is sent**, by decision of the owner: services name it differently (`thinking` at DeepSeek, `reasoning_effort` elsewhere), and freshgo knows no service. A model that reasons by default costs its time and tokens on every request.
- **No model is built in.** The README recommends one and says why.
- **The translation is not stored in the text of the entry**: clients of the Google Reader API, exports and what is handed out without a login keep the original.
- **Pieces of the text are sent to the service the administrator named** each time a reader asks for a translation; the README says so.
- **A text in the target language is told by asking the model**, one small request, beside which the title is asked for at once and in vain when the answer is yes: a model asked to translate such a text rewrites it (one of the two measured changed 72% of a text that was in the language already).

## Verification scenarios

- T1: `TestTranslateSettings`, `TestHelpKeepsSecrets` in `internal/config`.
- T3: `TestUnits`. T5: `TestPut` (marks moved; a mark missing, doubled, unknown, crossed, left open; code opened as a pair; a pair made whole; nothing).
- T2, T4, T5: `TestBlocks`, against an endpoint of the test that refuses a request with anything but the model and the messages: what is sent, an answer without one paragraph and with a mark dropped in another, a paragraph given up, a text given back as it was, a service that refuses (an error), a text of several requests.
- T6–T10: `TestSteps`, on both engines: the first step with its questions and its one small request of paragraphs, the entry itself unchanged and its other attributes kept, the original and back, the rest without asking twice, nothing asked when complete, the text changed, a text in the language already, a paragraph translated to one letter, a service that refuses in the middle and answers again, an entry there is not, the entry of another user.
- Not automated: the quality of the translations. It was judged by the owner on twelve entries translated by `deepseek-v4-flash` and `openai/gpt-6-luna`; the numbers are in issue #31.
