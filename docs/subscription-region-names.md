# Subscription region labels

Subscription labels use a flag and a concise Simplified Chinese region name,
followed by the operator's unchanged node name. These labels do not change the
region's ISO code, service identity, subscription URL, or public entry address.

- Prefer established short names: 香港, 澳门, 阿联酋, 波黑, 巴新, 巴勒斯坦, etc.
- Keep distinctions such as 英属/美属维尔京, 刚果（金）/刚果（布）, and 多米尼克/多米尼加.
- If no curated short name exists and the localized name exceeds six Unicode
  characters, use its ISO code for the subscription prefix; never clip the name
  into an ambiguous fragment. The region picker retains the full name in that
  case, and searches include full Chinese/English names, short labels and codes.
- Parse complete stored prefixes before shorter ones. In particular, shortening
  多米尼加共和国 must not leave 共和国 as part of the user's node name. The existing
  REALITY name reconciliation workflow applies canonical names to active nodes;
  no extra background worker or database schema change is introduced.

The policy is implemented in `internal/center/regions.go` and
`web/src/views/RegionCombobox.tsx`. Their regression cases cover short names,
retained distinctions, ISO-only labels and stored-name normalization. Local
tests/builds were not run for this change.

Naming references:

- [Unicode CLDR long and short territory names](https://cldr.unicode.org/translation/displaynames/countryregion-territory-names).
- [波黑](https://www.fmprc.gov.cn/web//gjhdq_676201/gj_676203/oz_678770/1206_678988/1206x0_678990/).
- [安巴](https://www.fmprc.gov.cn/web/gjhdq_676201/gj_676203/bmz_679954/1206_680008/1206x2_680028/201806/t20180605_9355640.shtml).
- [特多](https://www.fmprc.gov.cn/web/gjhdq_676201/gj_676203/bmz_679954/1206_680826/sbgx_680830/).
