package ttrsstest

// FilterExport is an OPML export with settings for the test account.
const FilterExport = `<?xml version="1.0" encoding="utf-8"?>
<opml version="1.0"><head><title>Tiny Tiny RSS Feed Export</title></head><body>
<outline text="News"><outline text="Tech">
<outline type="rss" text="Sub" xmlUrl="https://site2.example/feed.xml"/>
</outline><outline type="rss" text="Blog" xmlUrl="https://site1.example/feed.xml"/></outline>
<outline text="tt-rss-filters" schema-version="117">
<outline filter-type="2"><![CDATA[{"title":"Label updates","enabled":true,"match_any_rule":true,"inverse":false,"order_id":20,"rules":[{"reg_exp":"updates","filter_type":2,"inverse":false,"cat_filter":false,"match":[["News",true,false]]}],"actions":[{"action_id":7,"action_param":"Updates from filters"}]}]]></outline>
<outline filter-type="2"><![CDATA[{"title":"Read headlines","enabled":"t","match_any_rule":"f","inverse":"f","order_id":"10","rules":[{"reg_exp":"^Entry","filter_type":"1","inverse":"f","cat_filter":"f","feed":"Sub"}],"actions":[{"action_id":"2","action_param":""}]}]]></outline>
<outline filter-type="2"><![CDATA[{"title":"Missing feed","enabled":true,"order_id":5,"rules":[{"reg_exp":"^Entry","filter_type":1,"match":[["Unsubscribed",false,false]]}],"actions":[{"action_id":1,"action_param":""}]}]]></outline>
</outline></body></opml>`
