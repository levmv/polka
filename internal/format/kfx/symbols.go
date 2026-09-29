package kfx

// KFX field IDs and symbol values from the shared YJ symbol table.
const (
	fieldLanguage           = 10
	fieldFontFamily         = 11
	fieldColor              = 19
	fieldFixedWidth         = 66
	fieldFixedHeight        = 67
	fieldListStart          = 104
	fieldColumnSpan         = 148
	fieldRowSpan            = 149
	fieldTemplates          = 141
	fieldStyleEvents        = 142
	fieldOffset             = 143
	fieldLength             = 144
	fieldText               = 145
	fieldChildren           = 146
	fieldLocation           = 155
	fieldLayout             = 156
	fieldStyle              = 157
	fieldType               = 159
	fieldFormat             = 161
	fieldResourceLocation   = 165
	fieldReadingOrders      = 169
	fieldSections           = 170
	fieldResource           = 175
	fieldStory              = 176
	fieldLink               = 179
	fieldLinkPosition       = 183
	fieldURI                = 186
	fieldDirection          = 192
	fieldNavType            = 235
	fieldNavLabel           = 241
	fieldNavText            = 244
	fieldNavPosition        = 246
	fieldNavChildren        = 247
	fieldMetadataEntries    = 258
	fieldUnit               = 306
	fieldValue              = 307
	fieldNavContainers      = 392
	fieldTextIndex          = 403
	fieldCategories         = 491
	fieldMetadataKey        = 492
	fieldCategory           = 495
	fieldWritingMode        = 560
	fieldBoxAlign           = 580
	fieldAltText            = 584
	fieldLocalLocation      = 598
	fieldRender             = 601
	fieldTableVerticalAlign = 633
	fieldImageTiles         = 636
	fieldConnectedPages     = 655
	fieldPageSpread         = 656
	fieldTextDirection      = 682
	fieldRuby               = 757
	fieldRubyID             = 758
	fieldRubyRanges         = 759
	fieldHeadingLevel       = 790
)

const (
	fragmentText           = 145
	fragmentStyle          = 157
	fragmentResource       = 164
	fragmentLegacyMetadata = 258
	fragmentStory          = 259
	fragmentSection        = 260
	fragmentFont           = 262
	fragmentLink           = 266
	fragmentNavigation     = 389
	fragmentNavContainer   = 391
	fragmentNavItem        = 393
	fragmentRawMedia       = 417
	fragmentRawFont        = 418
	fragmentMetadata       = 490
	fragmentSettings       = 538
	fragmentContent        = 608
	fragmentRuby           = 756
)

const (
	symbolText      = "$269"
	symbolContainer = "$270"
	symbolImage     = "$271"
	symbolList      = "$276"
	symbolListItem  = "$277"
	symbolTable     = "$278"
	symbolRow       = "$279"
	symbolTableHead = "$151"
	symbolTableBody = "$454"
	symbolTableFoot = "$455"
	symbolRule      = "$596"
	symbolTOC       = "$212"
	symbolPageList  = "$237"
	symbolRTL       = "$375"
	symbolLTR       = "$376"
	symbolInline    = "$283"
	symbolVertical  = "$323"
	symbolFixed     = "$324"
	symbolScaleFit  = "$326"
	symbolSpread    = "$437"
	symbolFacing    = "$438"
)
