package cli

// analyticsGroup is a parent command holding related endpoints, such as the
// daily and monthly usage reports.
type analyticsGroup struct {
	use, short string
	specs      []analyticsSpec
}

// analyticsSpecs are the endpoints reached as `pcenter analytics <use>`.
// Field lists follow Microsoft's Store analytics API reference; each was also
// checked against FeedFlow's live data unless its doc comment says otherwise.
func analyticsSpecs() []analyticsSpec {
	return []analyticsSpec{
		{
			use:   "acquisitions",
			short: "List app acquisitions per day, week or month",
			long: "List app acquisitions (downloads) aggregated per day, week or month.\n\n" +
				"Every page is fetched, so the rows add up to the full total for the range.\n" +
				"Partner Center analytics lag by a day or two, so the most recent days may be missing.",
			endpoint:       "appacquisitions",
			dimensions:     []string{"osVersion", "market", "deviceType", "acquisitionType", "storeClient", "ageGroup", "gender", "orderName"},
			defaultGroupBy: "date",
			metrics:        []string{"acquisitionQuantity", "purchasePriceUSDAmount", "purchaseTaxUSDAmount"},
			aggregations:   []string{"day", "week", "month"},
			orderBy:        "date",
		},
	}
}

func analyticsGroups() []analyticsGroup {
	return nil
}
