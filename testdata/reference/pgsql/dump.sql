--
-- PostgreSQL database dump
--


-- Dumped from database version 17.11
-- Dumped by pg_dump version 17.11

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET transaction_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: freshrss_alice_category; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.freshrss_alice_category (
    id integer NOT NULL,
    name character varying(191) NOT NULL,
    kind smallint DEFAULT 0,
    "lastUpdate" bigint DEFAULT 0,
    error bigint DEFAULT 0,
    attributes text
);


--
-- Name: freshrss_alice_category_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.freshrss_alice_category_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: freshrss_alice_category_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.freshrss_alice_category_id_seq OWNED BY public.freshrss_alice_category.id;


--
-- Name: freshrss_alice_entry; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.freshrss_alice_entry (
    id bigint NOT NULL,
    guid character varying(767) NOT NULL,
    title character varying(8192) NOT NULL,
    author character varying(1024),
    content text,
    link character varying(16383) NOT NULL,
    date bigint,
    "lastSeen" bigint DEFAULT 0,
    "lastModified" bigint,
    "lastUserModified" bigint,
    hash bytea,
    is_read smallint DEFAULT 0 NOT NULL,
    is_favorite smallint DEFAULT 0 NOT NULL,
    id_feed integer,
    tags character varying(2048),
    attributes text
);


--
-- Name: freshrss_alice_entrytag; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.freshrss_alice_entrytag (
    id_tag integer NOT NULL,
    id_entry bigint NOT NULL
);


--
-- Name: freshrss_alice_entrytmp; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.freshrss_alice_entrytmp (
    id bigint NOT NULL,
    guid character varying(767) NOT NULL,
    title character varying(8192) NOT NULL,
    author character varying(1024),
    content text,
    link character varying(16383) NOT NULL,
    date bigint,
    "lastSeen" bigint DEFAULT 0,
    hash bytea,
    is_read smallint DEFAULT 0 NOT NULL,
    is_favorite smallint DEFAULT 0 NOT NULL,
    id_feed integer,
    tags character varying(2048),
    attributes text
);


--
-- Name: freshrss_alice_feed; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.freshrss_alice_feed (
    id integer NOT NULL,
    url character varying(32768) NOT NULL,
    kind smallint DEFAULT 0,
    category integer DEFAULT 0,
    name character varying(191) NOT NULL,
    website character varying(32768),
    description text,
    "lastUpdate" bigint DEFAULT 0,
    priority smallint DEFAULT 10 NOT NULL,
    "pathEntries" character varying(4096) DEFAULT NULL::character varying,
    "httpAuth" character varying(1024) DEFAULT NULL::character varying,
    error bigint DEFAULT 0,
    ttl integer DEFAULT 0 NOT NULL,
    attributes text,
    "cache_nbEntries" integer DEFAULT 0,
    "cache_nbUnreads" integer DEFAULT 0
);


--
-- Name: freshrss_alice_feed_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.freshrss_alice_feed_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: freshrss_alice_feed_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.freshrss_alice_feed_id_seq OWNED BY public.freshrss_alice_feed.id;


--
-- Name: freshrss_alice_tag; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.freshrss_alice_tag (
    id integer NOT NULL,
    name character varying(191) NOT NULL,
    attributes text
);


--
-- Name: freshrss_alice_tag_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.freshrss_alice_tag_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: freshrss_alice_tag_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.freshrss_alice_tag_id_seq OWNED BY public.freshrss_alice_tag.id;


--
-- Name: freshrss_bob_category; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.freshrss_bob_category (
    id integer NOT NULL,
    name character varying(191) NOT NULL,
    kind smallint DEFAULT 0,
    "lastUpdate" bigint DEFAULT 0,
    error bigint DEFAULT 0,
    attributes text
);


--
-- Name: freshrss_bob_category_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.freshrss_bob_category_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: freshrss_bob_category_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.freshrss_bob_category_id_seq OWNED BY public.freshrss_bob_category.id;


--
-- Name: freshrss_bob_entry; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.freshrss_bob_entry (
    id bigint NOT NULL,
    guid character varying(767) NOT NULL,
    title character varying(8192) NOT NULL,
    author character varying(1024),
    content text,
    link character varying(16383) NOT NULL,
    date bigint,
    "lastSeen" bigint DEFAULT 0,
    "lastModified" bigint,
    "lastUserModified" bigint,
    hash bytea,
    is_read smallint DEFAULT 0 NOT NULL,
    is_favorite smallint DEFAULT 0 NOT NULL,
    id_feed integer,
    tags character varying(2048),
    attributes text
);


--
-- Name: freshrss_bob_entrytag; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.freshrss_bob_entrytag (
    id_tag integer NOT NULL,
    id_entry bigint NOT NULL
);


--
-- Name: freshrss_bob_entrytmp; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.freshrss_bob_entrytmp (
    id bigint NOT NULL,
    guid character varying(767) NOT NULL,
    title character varying(8192) NOT NULL,
    author character varying(1024),
    content text,
    link character varying(16383) NOT NULL,
    date bigint,
    "lastSeen" bigint DEFAULT 0,
    hash bytea,
    is_read smallint DEFAULT 0 NOT NULL,
    is_favorite smallint DEFAULT 0 NOT NULL,
    id_feed integer,
    tags character varying(2048),
    attributes text
);


--
-- Name: freshrss_bob_feed; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.freshrss_bob_feed (
    id integer NOT NULL,
    url character varying(32768) NOT NULL,
    kind smallint DEFAULT 0,
    category integer DEFAULT 0,
    name character varying(191) NOT NULL,
    website character varying(32768),
    description text,
    "lastUpdate" bigint DEFAULT 0,
    priority smallint DEFAULT 10 NOT NULL,
    "pathEntries" character varying(4096) DEFAULT NULL::character varying,
    "httpAuth" character varying(1024) DEFAULT NULL::character varying,
    error bigint DEFAULT 0,
    ttl integer DEFAULT 0 NOT NULL,
    attributes text,
    "cache_nbEntries" integer DEFAULT 0,
    "cache_nbUnreads" integer DEFAULT 0
);


--
-- Name: freshrss_bob_feed_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.freshrss_bob_feed_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: freshrss_bob_feed_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.freshrss_bob_feed_id_seq OWNED BY public.freshrss_bob_feed.id;


--
-- Name: freshrss_bob_tag; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.freshrss_bob_tag (
    id integer NOT NULL,
    name character varying(191) NOT NULL,
    attributes text
);


--
-- Name: freshrss_bob_tag_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.freshrss_bob_tag_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: freshrss_bob_tag_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.freshrss_bob_tag_id_seq OWNED BY public.freshrss_bob_tag.id;


--
-- Name: freshrss_alice_category id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_alice_category ALTER COLUMN id SET DEFAULT nextval('public.freshrss_alice_category_id_seq'::regclass);


--
-- Name: freshrss_alice_feed id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_alice_feed ALTER COLUMN id SET DEFAULT nextval('public.freshrss_alice_feed_id_seq'::regclass);


--
-- Name: freshrss_alice_tag id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_alice_tag ALTER COLUMN id SET DEFAULT nextval('public.freshrss_alice_tag_id_seq'::regclass);


--
-- Name: freshrss_bob_category id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_bob_category ALTER COLUMN id SET DEFAULT nextval('public.freshrss_bob_category_id_seq'::regclass);


--
-- Name: freshrss_bob_feed id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_bob_feed ALTER COLUMN id SET DEFAULT nextval('public.freshrss_bob_feed_id_seq'::regclass);


--
-- Name: freshrss_bob_tag id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_bob_tag ALTER COLUMN id SET DEFAULT nextval('public.freshrss_bob_tag_id_seq'::regclass);


--
-- Data for Name: freshrss_alice_category; Type: TABLE DATA; Schema: public; Owner: -
--

INSERT INTO public.freshrss_alice_category VALUES (2, 'Blogs', 0, 0, 0, '{"position":0}');
INSERT INTO public.freshrss_alice_category VALUES (3, 'Scraped &amp; parsed', 0, 0, 0, '{"position":1}');
INSERT INTO public.freshrss_alice_category VALUES (1, 'Uncategorized', 0, 0, 0, NULL);


--
-- Data for Name: freshrss_alice_entry; Type: TABLE DATA; Schema: public; Owner: -
--

INSERT INTO public.freshrss_alice_entry VALUES (1791275023552071, 'urn:uuid:1225c695-cfb8-4ebb-aaaa-80da344efa6a', 'Identifier with surrounding spaces', '', 'Plain text content', 'http://feeds.freshgo.test/atom/spaces', 1788084000, 1791275023, NULL, NULL, '\x8c3df21a1682de9f32b1556bb37f826f', 0, 0, 1, '', '{"enclosures":[]}');
INSERT INTO public.freshrss_alice_entry VALUES (1791275023552074, 'html-1', 'First scraped article', ';Dora Scraper', '<div data-sanitized-class="body"><p>Body of the <strong>first</strong> article.</p><p>Second paragraph.</p></div>', 'http://feeds.freshgo.test/page/one', 1788256800, 1791275023, NULL, NULL, '\x6f7f875ccea75a242d5ccd7a33e16240', 0, 0, 3, '#alpha #beta', '{"thumbnail":{"url":"http://feeds.freshgo.test/page/one.jpg"},"enclosures":[{"url":"http://feeds.freshgo.test/page/one.jpg","medium":"image"}]}');
INSERT INTO public.freshrss_alice_entry VALUES (1791275023552075, 'xml-1', 'First XML record', ';Fay Catalog', 'Text of the first record', 'http://feeds.freshgo.test/data/1', 1788260400, 1791275023, NULL, NULL, '\x76d2719ebf608afba4e20aa00d417221', 0, 0, 4, '#one #two', '{"enclosures":[]}');
INSERT INTO public.freshrss_alice_entry VALUES (1791275023552077, 'jsonfeed-1', 'First JSON Feed item', '', '<p>HTML content of the <em>first</em> item.</p>', 'http://feeds.freshgo.test/jsonfeed/1', 1788267600, 1791275023, NULL, NULL, '\xcf8666907990981addcf4db797f755fd', 0, 0, 5, '#json #feed', '{"thumbnail":{"url":"http://feeds.freshgo.test/jsonfeed/1.png"},"enclosures":[{"url":"http://feeds.freshgo.test/jsonfeed/1.png","medium":"image"}]}');
INSERT INTO public.freshrss_alice_entry VALUES (1791275023552078, 'emb-1', 'First embedded article', ';Lu Embed', '<p>Embedded body one.</p>', 'http://feeds.freshgo.test/embedded/1', 1788274800, 1791275023, NULL, NULL, '\x625c66fca2bf620106a1922bb426c8d5', 0, 0, 7, '', '{"enclosures":[]}');
INSERT INTO public.freshrss_alice_entry VALUES (1791275023552079, 'http://feeds.freshgo.test/atom/query?a=1&amp;b=2&amp;=', 'Identifier with an ampersand &amp; non-ASCII: Привет', ';Иван Петров; O''Neil &amp; Sons', 'Summary with &lt;angle brackets&gt; &amp; &quot;quotes&quot;.', 'http://feeds.freshgo.test/atom/query?a=1&amp;b=2', 1788325200, 1791275023, NULL, NULL, '\x5c38176ef0cd9047692c5bf5b456cfb3', 0, 0, 1, '', '{"enclosures":[]}');
INSERT INTO public.freshrss_alice_entry VALUES (1791275023552082, 'html-2', 'Second &amp; last', ';Ed Parser', '<div data-sanitized-class="body"><p>Body of the second article.</p></div>', 'http://feeds.freshgo.test/page/two?a=1&amp;b=2', 1788343200, 1791275023, NULL, NULL, '\xeedcbd66adaaf7d4f06f8cb6891f4735', 0, 0, 3, '#gamma', '{"enclosures":[]}');
INSERT INTO public.freshrss_alice_entry VALUES (1791275023552083, 'api-1', 'First API post', ';Jo Api', '<p>Body of the first API post.</p>', 'http://feeds.freshgo.test/api/1', 1788343200, 1791275023, NULL, NULL, '\x1d5974b093b96c4fe01491129b60d9d0', 0, 0, 6, '#x #y', '{"thumbnail":{"url":"http://feeds.freshgo.test/api/1.jpg"},"enclosures":[{"url":"http://feeds.freshgo.test/api/1.jpg","medium":"image"}]}');
INSERT INTO public.freshrss_alice_entry VALUES (1791275023552084, 'xml-2', 'Second XML record &amp; more', ';Gus Catalog', 'Text of the second record', 'http://feeds.freshgo.test/data/2', 1788346800, 1791275023, NULL, NULL, '\xc8a0a2122176e9a0ab8d8786333b089b', 0, 0, 4, '#three', '{"enclosures":[]}');
INSERT INTO public.freshrss_alice_entry VALUES (1791275023552085, 'http://feeds.freshgo.test/jsonfeed/2?a=1&amp;b=2', 'Second item with text content &amp; an ampersand', '', '', 'http://feeds.freshgo.test/jsonfeed/2?a=1&amp;b=2', 1788346800, 1791275023, NULL, NULL, '\x91eafa5d8eb4b443931032f8534f41c7', 0, 0, 5, '', '{"enclosures":[]}');
INSERT INTO public.freshrss_alice_entry VALUES (1791275023552086, 'emb-2', 'Second embedded article', ';Mo Embed', '<p>Embedded body two.</p>', 'http://feeds.freshgo.test/embedded/2', 1788361200, 1791275023, NULL, NULL, '\xc748f7284d82a50df9d07776448e7f83', 0, 0, 7, '', '{"enclosures":[]}');
INSERT INTO public.freshrss_alice_entry VALUES (1791275023552088, 'https://github.com/juev/freshgo/issues/1', 'Identifier on a force-https domain', '', '<p>XHTML <em>content</em> with an image <img src="https://github.com/juev/freshgo/issues/img/pic.png" alt="pic"></p>', 'https://github.com/juev/freshgo/issues/1', 1788429600, 1791275023, NULL, NULL, '\x37c5b4d6c49273661d357669978bcae8', 0, 0, 1, '', '{"enclosures":[]}');
INSERT INTO public.freshrss_alice_entry VALUES (1791275023552089, 'api-2', 'Second API post', ';Kit Api', '<p>Body of the second API post.</p>', 'http://feeds.freshgo.test/api/2', 1788429600, 1791275023, NULL, NULL, '\x611c2656b5716b31ef71a5cd6abf4b92', 0, 0, 6, '#z', '{"enclosures":[]}');
INSERT INTO public.freshrss_alice_entry VALUES (1791275023552090, 'id &lt;3&gt; &quot;quoted&quot; ''single'' &amp; done', 'Guid with markup characters', '', 'Guid with characters that are HTML-encoded.', 'http://feeds.freshgo.test/rss/markup', 1788436800, 1791275023, NULL, NULL, '\x140a07167b698203ed5a6eb774f07d31', 0, 0, 2, '', '{"thumbnail":{"url":"http://feeds.freshgo.test/rss/thumb.jpg"},"enclosures":[]}');
INSERT INTO public.freshrss_alice_entry VALUES (1791275023552091, 'https://blog.wordpress.com/?p=7', 'Guid on a force-https subdomain', '', 'Subdomain of a domain from the force-https list.', 'https://blog.wordpress.com/post', 1788523200, 1791275023, NULL, NULL, '\xb717ccbbcb1e86a811a3a62d4cc04803', 0, 0, 2, '', '{"enclosures":[]}');
INSERT INTO public.freshrss_alice_entry VALUES (1791275023552092, 'https://www.example.net/custom?id=9', 'Guid on a domain from the custom force-https list', '', 'The domain is listed in data/force-https.txt of the reference installation.', 'https://www.example.net/custom', 1788609600, 1791275023, NULL, NULL, '\xd61c82c19768cd876f00728faadaf2e4', 0, 0, 2, '', '{"enclosures":[]}');
INSERT INTO public.freshrss_alice_entry VALUES (1791275023552073, 'tag:feeds.freshgo.test,2026:atom/plain', 'Plain entry', ';Alice Author', '<p>First paragraph with an <a href="http://feeds.freshgo.test/atom/relative">relative link</a>.</p>', 'http://feeds.freshgo.test/atom/plain', 1788249600, 1791275023, NULL, 1791275024, '\x6aab42ef8a566b7c50ae7c810594bc7d', 0, 1, 1, '#go #rss readers', '{"enclosures":[]}');
INSERT INTO public.freshrss_alice_entry VALUES (1791275023552081, 'post-42', 'Opaque guid', ';carol@example.org (Carol)', '<p>Escaped HTML description</p>', 'http://feeds.freshgo.test/rss/opaque', 1788343200, 1791275023, NULL, 1791275024, '\x9d09917d93c4c29801c59e6ed89178a9', 0, 1, 2, '', '{"enclosures":[{"url":"http://feeds.freshgo.test/rss/audio.mp3","type":"audio/mpeg","length":12345}]}');
INSERT INTO public.freshrss_alice_entry VALUES (1791275023552076, 'http://feeds.freshgo.test/rss/permalink', 'Permalink guid', ';Bob Writer', '<p>Full <b>content</b> with <img src="http://feeds.freshgo.test/rss/img.png"> and a <a href="http://feeds.freshgo.test/rss/relative/page">link</a>.</p>', 'http://feeds.freshgo.test/rss/permalink', 1788264000, 1791275023, NULL, 1791275024, '\xfdeb6fba94fc5067d3031fbb23d34785', 1, 0, 2, '#news #tech &amp; science', '{"enclosures":[]}');
INSERT INTO public.freshrss_alice_entry VALUES (1791275023552072, 'f0e70b21e3038ec24999d8987f3030f4859a2e5d', 'First without guid', '', 'First.', 'http://feeds.freshgo.test/noid/first?x=1&amp;y=2', 1788242400, 1791275023, NULL, 1791275024, '\x81dbf47377d14caf5b81d5988cebd3a3', 1, 0, 8, '', '{"enclosures":[]}');
INSERT INTO public.freshrss_alice_entry VALUES (1791275023552080, '0060234b88e085ad6c747ecad77d3334b03ddd8a', 'Second without guid', '', 'Second.', 'http://feeds.freshgo.test/noid/second', 1788328800, 1791275023, NULL, 1791275024, '\xaceec36d1f3624611b84b58e61795ea3', 1, 0, 8, '', '{"enclosures":[]}');
INSERT INTO public.freshrss_alice_entry VALUES (1791275023552087, 'dfc9f68062269afebc8ee3feedf00876b1950a30', 'Third on a force-https domain', '', 'Third.', 'https://youtube.com/watch?v=abc', 1788415200, 1791275023, NULL, 1791275024, '\xc793a0869835054366b2cd63f063d1e4', 1, 0, 8, '', '{"enclosures":[]}');


--
-- Data for Name: freshrss_alice_entrytag; Type: TABLE DATA; Schema: public; Owner: -
--

INSERT INTO public.freshrss_alice_entrytag VALUES (1, 1791275023552073);
INSERT INTO public.freshrss_alice_entrytag VALUES (1, 1791275023552077);
INSERT INTO public.freshrss_alice_entrytag VALUES (2, 1791275023552081);


--
-- Data for Name: freshrss_alice_entrytmp; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: freshrss_alice_feed; Type: TABLE DATA; Schema: public; Owner: -
--

INSERT INTO public.freshrss_alice_feed VALUES (2, 'http://feeds.freshgo.test/rss.xml', 0, 2, 'RSS corpus', 'http://feeds.freshgo.test/rss/', 'RSS 2.0 feed with guid elements', 1791275023, 10, '', '', 0, 7200, '{"SimplePieHash":"1ad231e045cdb5126169559e3d6e85e90a4f40d3","customFavicon":true,"customFaviconDisallowDel":false}', 5, 4);
INSERT INTO public.freshrss_alice_feed VALUES (8, 'http://feeds.freshgo.test/rss-noid.xml', 0, 1, 'No identifiers', 'http://feeds.freshgo.test/noid/', 'Items have no guid: the key falls back to link and date', 1791275023, -5, '', '', 0, 0, '{"SimplePieHash":"5b9dc45a4cb1a88e30211f8dda27e79af8ea23db"}', 3, 0);
INSERT INTO public.freshrss_alice_feed VALUES (5, 'http://feeds.freshgo.test/feed.json', 25, 3, 'JSON Feed', 'http://feeds.freshgo.test/jsonfeed/', 'RSS feed of JSON Feed corpus', 1791275023, 10, '', '', 0, 0, '[]', 2, 2);
INSERT INTO public.freshrss_alice_feed VALUES (6, 'http://feeds.freshgo.test/api.json', 30, 3, 'JSON API', 'http://feeds.freshgo.test/api/', 'RSS feed of JSON API', 1791275023, 10, '', '', 0, 0, '{"json_dotnotation":{"item":"data.posts","itemTitle":"headline","itemContent":"body.html","itemUri":"permalink","itemAuthor":"writer.display","itemTimestamp":"created","itemTimeFormat":"U","itemThumbnail":"cover","itemCategories":"labels","itemUid":"key"}}', 2, 2);
INSERT INTO public.freshrss_alice_feed VALUES (1, 'http://feeds.freshgo.test/atom.xml', 0, 2, 'Atom corpus', 'http://feeds.freshgo.test/atom/', 'Atom feed with stable identifiers', 1791275023, 20, '', '', 0, 0, '{"SimplePieHash":"2b6d159abc7f9d9421cd766b769de1e358077bb2"}', 4, 4);
INSERT INTO public.freshrss_alice_feed VALUES (7, 'http://feeds.freshgo.test/embedded.html', 35, 3, 'Embedded JSON', 'http://feeds.freshgo.test/embedded.html', 'RSS feed of Embedded JSON', 1791275023, 10, '', '', 0, 0, '{"json_dotnotation":{"item":"0.articles","itemTitle":"name","itemContent":"html","itemUri":"href","itemAuthor":"by","itemTimestamp":"ts","itemUid":"slug"},"xPathToJson":"//script[@id=\"state\"]"}', 2, 2);
INSERT INTO public.freshrss_alice_feed VALUES (3, 'http://feeds.freshgo.test/page.html', 10, 3, 'HTML page', 'http://feeds.freshgo.test/page.html', 'RSS feed of HTML page', 1791275023, 10, '', '', 0, 0, '{"xpath":{"item":"//article","itemTitle":"descendant::h2","itemContent":"descendant::div[@class=\"body\"]","itemUri":"descendant::h2/a/@href","itemAuthor":"descendant::span[@class=\"author\"]","itemTimestamp":"descendant::time/@datetime","itemThumbnail":"descendant::img/@src","itemCategories":"descendant::ul[@class=\"tags\"]/li","itemUid":"@data-id"}}', 2, 2);
INSERT INTO public.freshrss_alice_feed VALUES (4, 'http://feeds.freshgo.test/data.xml', 15, 3, 'XML catalog', 'http://feeds.freshgo.test/data/', 'RSS feed of XML catalog', 1791275023, 10, '', '', 0, 0, '{"xpath":{"item":"//record","itemTitle":"name","itemContent":"text","itemUri":"url","itemAuthor":"by","itemTimestamp":"when","itemTimeFormat":"Y-m-d H:i:s","itemCategories":"topic","itemUid":"@uid"}}', 2, 2);


--
-- Data for Name: freshrss_alice_tag; Type: TABLE DATA; Schema: public; Owner: -
--

INSERT INTO public.freshrss_alice_tag VALUES (1, 'later', '[]');
INSERT INTO public.freshrss_alice_tag VALUES (2, 'work &amp; play', '[]');


--
-- Data for Name: freshrss_bob_category; Type: TABLE DATA; Schema: public; Owner: -
--

INSERT INTO public.freshrss_bob_category VALUES (2, 'News', 0, 0, 0, '{"position":0}');
INSERT INTO public.freshrss_bob_category VALUES (1, 'Uncategorized', 0, 0, 0, NULL);


--
-- Data for Name: freshrss_bob_entry; Type: TABLE DATA; Schema: public; Owner: -
--

INSERT INTO public.freshrss_bob_entry VALUES (1791275023868476, 'jsonfeed-1', 'First JSON Feed item', '', '<p>HTML content of the <em>first</em> item.</p>', 'http://feeds.freshgo.test/jsonfeed/1', 1788267600, 1791275023, NULL, NULL, '\xcf8666907990981addcf4db797f755fd', 0, 0, 2, '#json #feed', '{"thumbnail":{"url":"http://feeds.freshgo.test/jsonfeed/1.png"},"enclosures":[{"url":"http://feeds.freshgo.test/jsonfeed/1.png","medium":"image"}]}');
INSERT INTO public.freshrss_bob_entry VALUES (1791275023868478, 'post-42', 'Opaque guid', ';carol@example.org (Carol)', '<p>Escaped HTML description</p>', 'http://feeds.freshgo.test/rss/opaque', 1788343200, 1791275023, NULL, NULL, '\x9d09917d93c4c29801c59e6ed89178a9', 0, 0, 1, '', '{"enclosures":[{"url":"http://feeds.freshgo.test/rss/audio.mp3","type":"audio/mpeg","length":12345}]}');
INSERT INTO public.freshrss_bob_entry VALUES (1791275023868479, 'http://feeds.freshgo.test/jsonfeed/2?a=1&amp;b=2', 'Second item with text content &amp; an ampersand', '', '', 'http://feeds.freshgo.test/jsonfeed/2?a=1&amp;b=2', 1788346800, 1791275023, NULL, NULL, '\x91eafa5d8eb4b443931032f8534f41c7', 0, 0, 2, '', '{"enclosures":[]}');
INSERT INTO public.freshrss_bob_entry VALUES (1791275023868481, 'id &lt;3&gt; &quot;quoted&quot; ''single'' &amp; done', 'Guid with markup characters', '', 'Guid with characters that are HTML-encoded.', 'http://feeds.freshgo.test/rss/markup', 1788436800, 1791275023, NULL, NULL, '\x140a07167b698203ed5a6eb774f07d31', 0, 0, 1, '', '{"thumbnail":{"url":"http://feeds.freshgo.test/rss/thumb.jpg"},"enclosures":[]}');
INSERT INTO public.freshrss_bob_entry VALUES (1791275023868482, 'https://blog.wordpress.com/?p=7', 'Guid on a force-https subdomain', '', 'Subdomain of a domain from the force-https list.', 'https://blog.wordpress.com/post', 1788523200, 1791275023, NULL, NULL, '\xb717ccbbcb1e86a811a3a62d4cc04803', 0, 0, 1, '', '{"enclosures":[]}');
INSERT INTO public.freshrss_bob_entry VALUES (1791275023868483, 'https://www.example.net/custom?id=9', 'Guid on a domain from the custom force-https list', '', 'The domain is listed in data/force-https.txt of the reference installation.', 'https://www.example.net/custom', 1788609600, 1791275023, NULL, NULL, '\xd61c82c19768cd876f00728faadaf2e4', 0, 0, 1, '', '{"enclosures":[]}');
INSERT INTO public.freshrss_bob_entry VALUES (1791275023868475, 'http://feeds.freshgo.test/rss/permalink', 'Permalink guid', ';Bob Writer', '<p>Full <b>content</b> with <img src="http://feeds.freshgo.test/rss/img.png"> and a <a href="http://feeds.freshgo.test/rss/relative/page">link</a>.</p>', 'http://feeds.freshgo.test/rss/permalink', 1788264000, 1791275023, NULL, 1791275024, '\xfdeb6fba94fc5067d3031fbb23d34785', 0, 1, 1, '#news #tech &amp; science', '{"enclosures":[]}');
INSERT INTO public.freshrss_bob_entry VALUES (1791275023868473, 'urn:uuid:1225c695-cfb8-4ebb-aaaa-80da344efa6a', 'Identifier with surrounding spaces', '', 'Plain text content', 'http://feeds.freshgo.test/atom/spaces', 1788084000, 1791275023, NULL, 1791275024, '\x8c3df21a1682de9f32b1556bb37f826f', 1, 0, 3, '', '{"enclosures":[]}');
INSERT INTO public.freshrss_bob_entry VALUES (1791275023868474, 'tag:feeds.freshgo.test,2026:atom/plain', 'Plain entry', ';Alice Author', '<p>First paragraph with an <a href="http://feeds.freshgo.test/atom/relative">relative link</a>.</p>', 'http://feeds.freshgo.test/atom/plain', 1788249600, 1791275023, NULL, 1791275024, '\x6aab42ef8a566b7c50ae7c810594bc7d', 1, 0, 3, '#go #rss readers', '{"enclosures":[]}');
INSERT INTO public.freshrss_bob_entry VALUES (1791275023868477, 'http://feeds.freshgo.test/atom/query?a=1&amp;b=2&amp;=', 'Identifier with an ampersand &amp; non-ASCII: Привет', ';Иван Петров; O''Neil &amp; Sons', 'Summary with &lt;angle brackets&gt; &amp; &quot;quotes&quot;.', 'http://feeds.freshgo.test/atom/query?a=1&amp;b=2', 1788325200, 1791275023, NULL, 1791275024, '\x5c38176ef0cd9047692c5bf5b456cfb3', 1, 0, 3, '', '{"enclosures":[]}');
INSERT INTO public.freshrss_bob_entry VALUES (1791275023868480, 'https://github.com/juev/freshgo/issues/1', 'Identifier on a force-https domain', '', '<p>XHTML <em>content</em> with an image <img src="https://github.com/juev/freshgo/issues/img/pic.png" alt="pic"></p>', 'https://github.com/juev/freshgo/issues/1', 1788429600, 1791275023, NULL, 1791275024, '\x37c5b4d6c49273661d357669978bcae8', 1, 0, 3, '', '{"enclosures":[]}');


--
-- Data for Name: freshrss_bob_entrytag; Type: TABLE DATA; Schema: public; Owner: -
--

INSERT INTO public.freshrss_bob_entrytag VALUES (1, 1791275023868480);


--
-- Data for Name: freshrss_bob_entrytmp; Type: TABLE DATA; Schema: public; Owner: -
--



--
-- Data for Name: freshrss_bob_feed; Type: TABLE DATA; Schema: public; Owner: -
--

INSERT INTO public.freshrss_bob_feed VALUES (1, 'http://feeds.freshgo.test/rss.xml', 0, 2, 'RSS corpus (bob)', 'http://feeds.freshgo.test/rss/', 'RSS 2.0 feed with guid elements', 1791275023, 10, '', '', 0, 0, '{"SimplePieHash":"1ad231e045cdb5126169559e3d6e85e90a4f40d3"}', 5, 5);
INSERT INTO public.freshrss_bob_feed VALUES (2, 'http://feeds.freshgo.test/feed.json', 25, 2, 'JSON Feed (bob)', 'http://feeds.freshgo.test/jsonfeed/', 'RSS feed of JSON Feed corpus', 1791275023, 0, '', '', 0, 0, '[]', 2, 2);
INSERT INTO public.freshrss_bob_feed VALUES (3, 'http://feeds.freshgo.test/atom.xml', 0, 2, 'Atom corpus (bob)', 'http://feeds.freshgo.test/atom/', 'Atom feed with stable identifiers', 1791275023, 10, '', '', 0, 0, '{"SimplePieHash":"2b6d159abc7f9d9421cd766b769de1e358077bb2"}', 4, 0);


--
-- Data for Name: freshrss_bob_tag; Type: TABLE DATA; Schema: public; Owner: -
--

INSERT INTO public.freshrss_bob_tag VALUES (1, 'later', '[]');


--
-- Name: freshrss_alice_category_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.freshrss_alice_category_id_seq', 3, true);


--
-- Name: freshrss_alice_feed_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.freshrss_alice_feed_id_seq', 8, true);


--
-- Name: freshrss_alice_tag_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.freshrss_alice_tag_id_seq', 2, true);


--
-- Name: freshrss_bob_category_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.freshrss_bob_category_id_seq', 2, true);


--
-- Name: freshrss_bob_feed_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.freshrss_bob_feed_id_seq', 3, true);


--
-- Name: freshrss_bob_tag_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.freshrss_bob_tag_id_seq', 1, true);


--
-- Name: freshrss_alice_category freshrss_alice_category_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_alice_category
    ADD CONSTRAINT freshrss_alice_category_name_key UNIQUE (name);


--
-- Name: freshrss_alice_category freshrss_alice_category_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_alice_category
    ADD CONSTRAINT freshrss_alice_category_pkey PRIMARY KEY (id);


--
-- Name: freshrss_alice_entry freshrss_alice_entry_id_feed_guid_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_alice_entry
    ADD CONSTRAINT freshrss_alice_entry_id_feed_guid_key UNIQUE (id_feed, guid);


--
-- Name: freshrss_alice_entry freshrss_alice_entry_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_alice_entry
    ADD CONSTRAINT freshrss_alice_entry_pkey PRIMARY KEY (id);


--
-- Name: freshrss_alice_entrytag freshrss_alice_entrytag_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_alice_entrytag
    ADD CONSTRAINT freshrss_alice_entrytag_pkey PRIMARY KEY (id_tag, id_entry);


--
-- Name: freshrss_alice_entrytmp freshrss_alice_entrytmp_id_feed_guid_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_alice_entrytmp
    ADD CONSTRAINT freshrss_alice_entrytmp_id_feed_guid_key UNIQUE (id_feed, guid);


--
-- Name: freshrss_alice_entrytmp freshrss_alice_entrytmp_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_alice_entrytmp
    ADD CONSTRAINT freshrss_alice_entrytmp_pkey PRIMARY KEY (id);


--
-- Name: freshrss_alice_feed freshrss_alice_feed_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_alice_feed
    ADD CONSTRAINT freshrss_alice_feed_pkey PRIMARY KEY (id);


--
-- Name: freshrss_alice_tag freshrss_alice_tag_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_alice_tag
    ADD CONSTRAINT freshrss_alice_tag_name_key UNIQUE (name);


--
-- Name: freshrss_alice_tag freshrss_alice_tag_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_alice_tag
    ADD CONSTRAINT freshrss_alice_tag_pkey PRIMARY KEY (id);


--
-- Name: freshrss_bob_category freshrss_bob_category_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_bob_category
    ADD CONSTRAINT freshrss_bob_category_name_key UNIQUE (name);


--
-- Name: freshrss_bob_category freshrss_bob_category_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_bob_category
    ADD CONSTRAINT freshrss_bob_category_pkey PRIMARY KEY (id);


--
-- Name: freshrss_bob_entry freshrss_bob_entry_id_feed_guid_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_bob_entry
    ADD CONSTRAINT freshrss_bob_entry_id_feed_guid_key UNIQUE (id_feed, guid);


--
-- Name: freshrss_bob_entry freshrss_bob_entry_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_bob_entry
    ADD CONSTRAINT freshrss_bob_entry_pkey PRIMARY KEY (id);


--
-- Name: freshrss_bob_entrytag freshrss_bob_entrytag_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_bob_entrytag
    ADD CONSTRAINT freshrss_bob_entrytag_pkey PRIMARY KEY (id_tag, id_entry);


--
-- Name: freshrss_bob_entrytmp freshrss_bob_entrytmp_id_feed_guid_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_bob_entrytmp
    ADD CONSTRAINT freshrss_bob_entrytmp_id_feed_guid_key UNIQUE (id_feed, guid);


--
-- Name: freshrss_bob_entrytmp freshrss_bob_entrytmp_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_bob_entrytmp
    ADD CONSTRAINT freshrss_bob_entrytmp_pkey PRIMARY KEY (id);


--
-- Name: freshrss_bob_feed freshrss_bob_feed_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_bob_feed
    ADD CONSTRAINT freshrss_bob_feed_pkey PRIMARY KEY (id);


--
-- Name: freshrss_bob_tag freshrss_bob_tag_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_bob_tag
    ADD CONSTRAINT freshrss_bob_tag_name_key UNIQUE (name);


--
-- Name: freshrss_bob_tag freshrss_bob_tag_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_bob_tag
    ADD CONSTRAINT freshrss_bob_tag_pkey PRIMARY KEY (id);


--
-- Name: freshrss_alice_entry_feed_read_index; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX freshrss_alice_entry_feed_read_index ON public.freshrss_alice_entry USING btree (id_feed, is_read);


--
-- Name: freshrss_alice_entry_lastSeen_index; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "freshrss_alice_entry_lastSeen_index" ON public.freshrss_alice_entry USING btree ("lastSeen");


--
-- Name: freshrss_alice_entry_last_modified_index; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX freshrss_alice_entry_last_modified_index ON public.freshrss_alice_entry USING btree ("lastModified");


--
-- Name: freshrss_alice_entry_last_user_modified_index; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX freshrss_alice_entry_last_user_modified_index ON public.freshrss_alice_entry USING btree ("lastUserModified");


--
-- Name: freshrss_alice_entrytag_id_entry_index; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX freshrss_alice_entrytag_id_entry_index ON public.freshrss_alice_entrytag USING btree (id_entry);


--
-- Name: freshrss_alice_entrytmp_date_index; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX freshrss_alice_entrytmp_date_index ON public.freshrss_alice_entrytmp USING btree (date);


--
-- Name: freshrss_alice_is_favorite_index; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX freshrss_alice_is_favorite_index ON public.freshrss_alice_entry USING btree (is_favorite);


--
-- Name: freshrss_alice_is_read_index; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX freshrss_alice_is_read_index ON public.freshrss_alice_entry USING btree (is_read);


--
-- Name: freshrss_alice_name_index; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX freshrss_alice_name_index ON public.freshrss_alice_feed USING btree (name);


--
-- Name: freshrss_alice_priority_index; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX freshrss_alice_priority_index ON public.freshrss_alice_feed USING btree (priority);


--
-- Name: freshrss_bob_entry_feed_read_index; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX freshrss_bob_entry_feed_read_index ON public.freshrss_bob_entry USING btree (id_feed, is_read);


--
-- Name: freshrss_bob_entry_lastSeen_index; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX "freshrss_bob_entry_lastSeen_index" ON public.freshrss_bob_entry USING btree ("lastSeen");


--
-- Name: freshrss_bob_entry_last_modified_index; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX freshrss_bob_entry_last_modified_index ON public.freshrss_bob_entry USING btree ("lastModified");


--
-- Name: freshrss_bob_entry_last_user_modified_index; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX freshrss_bob_entry_last_user_modified_index ON public.freshrss_bob_entry USING btree ("lastUserModified");


--
-- Name: freshrss_bob_entrytag_id_entry_index; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX freshrss_bob_entrytag_id_entry_index ON public.freshrss_bob_entrytag USING btree (id_entry);


--
-- Name: freshrss_bob_entrytmp_date_index; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX freshrss_bob_entrytmp_date_index ON public.freshrss_bob_entrytmp USING btree (date);


--
-- Name: freshrss_bob_is_favorite_index; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX freshrss_bob_is_favorite_index ON public.freshrss_bob_entry USING btree (is_favorite);


--
-- Name: freshrss_bob_is_read_index; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX freshrss_bob_is_read_index ON public.freshrss_bob_entry USING btree (is_read);


--
-- Name: freshrss_bob_name_index; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX freshrss_bob_name_index ON public.freshrss_bob_feed USING btree (name);


--
-- Name: freshrss_bob_priority_index; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX freshrss_bob_priority_index ON public.freshrss_bob_feed USING btree (priority);


--
-- Name: freshrss_alice_entry freshrss_alice_entry_id_feed_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_alice_entry
    ADD CONSTRAINT freshrss_alice_entry_id_feed_fkey FOREIGN KEY (id_feed) REFERENCES public.freshrss_alice_feed(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: freshrss_alice_entrytag freshrss_alice_entrytag_id_entry_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_alice_entrytag
    ADD CONSTRAINT freshrss_alice_entrytag_id_entry_fkey FOREIGN KEY (id_entry) REFERENCES public.freshrss_alice_entry(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: freshrss_alice_entrytag freshrss_alice_entrytag_id_tag_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_alice_entrytag
    ADD CONSTRAINT freshrss_alice_entrytag_id_tag_fkey FOREIGN KEY (id_tag) REFERENCES public.freshrss_alice_tag(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: freshrss_alice_entrytmp freshrss_alice_entrytmp_id_feed_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_alice_entrytmp
    ADD CONSTRAINT freshrss_alice_entrytmp_id_feed_fkey FOREIGN KEY (id_feed) REFERENCES public.freshrss_alice_feed(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: freshrss_alice_feed freshrss_alice_feed_category_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_alice_feed
    ADD CONSTRAINT freshrss_alice_feed_category_fkey FOREIGN KEY (category) REFERENCES public.freshrss_alice_category(id) ON UPDATE CASCADE ON DELETE SET NULL;


--
-- Name: freshrss_bob_entry freshrss_bob_entry_id_feed_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_bob_entry
    ADD CONSTRAINT freshrss_bob_entry_id_feed_fkey FOREIGN KEY (id_feed) REFERENCES public.freshrss_bob_feed(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: freshrss_bob_entrytag freshrss_bob_entrytag_id_entry_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_bob_entrytag
    ADD CONSTRAINT freshrss_bob_entrytag_id_entry_fkey FOREIGN KEY (id_entry) REFERENCES public.freshrss_bob_entry(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: freshrss_bob_entrytag freshrss_bob_entrytag_id_tag_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_bob_entrytag
    ADD CONSTRAINT freshrss_bob_entrytag_id_tag_fkey FOREIGN KEY (id_tag) REFERENCES public.freshrss_bob_tag(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: freshrss_bob_entrytmp freshrss_bob_entrytmp_id_feed_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_bob_entrytmp
    ADD CONSTRAINT freshrss_bob_entrytmp_id_feed_fkey FOREIGN KEY (id_feed) REFERENCES public.freshrss_bob_feed(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: freshrss_bob_feed freshrss_bob_feed_category_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.freshrss_bob_feed
    ADD CONSTRAINT freshrss_bob_feed_category_fkey FOREIGN KEY (category) REFERENCES public.freshrss_bob_category(id) ON UPDATE CASCADE ON DELETE SET NULL;


--
-- PostgreSQL database dump complete
--


