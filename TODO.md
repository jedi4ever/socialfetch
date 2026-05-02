- Google search, ask
- Perplexity
- Throttle / random timing extension
- Name of project - socialfetch - social-curl?
- Publish as skill
- Homebre
- Rename extension / extension permissions
- Which HTML to Markdown / use library ?

   - The serpapi and tavily ask providers also lack live tests +
     swallow error bodies + hardcode model + don't wire Instructions.
     Same fix applies to each, but doing them in this commit risks a
     20-minute live-test runtime when one of them is misconfigured.
     Queue as separate commits.
     - groundingSupports / inline citations: Gemini returns rich
     segment-level citation data we currently ignore. Worth a follow-up
     for richer markdown rendering with inline [1] markers, but
     unrelated to making the basic ask work.
