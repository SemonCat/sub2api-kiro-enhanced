package service

// Sanitized production regression corpus.
//
// Provenance: pulled via an authorized read-only SQL query against the
// service's own inner usage_logs table (native kiro_credits, input_tokens,
// output_tokens columns only) for the exact incident window this fix
// addresses -- two concurrent Claude Opus cron sessions behind the same
// outer key that exhibited the regression (reported cache rate ~69.5% before
// the estimator regressed, ~0 after). Only the three numeric columns below
// were extracted, in call order. No prompt/response content, account or
// session identifiers, trace/request IDs, timestamps, or credentials are
// included -- those columns were never selected. kiroRealTrafficSessionA has
// 60 rows and kiroRealTrafficSessionB has 61, matching the incident's
// reported per-session metered call counts.
type kiroRealTrafficRow struct {
	// Input is the native input token count reported for this call.
	Input int
	// Output is the native output token count reported for this call.
	// Real agent traffic does not hold this fixed or move it monotonically;
	// note the swings from single digits to 1000+ within one session.
	Output int
	// Credits is the native Kiro metering value reported for this call. This
	// is the only cache-efficiency signal available for real traffic --
	// native cache_read/cache_creation were 0 for every one of these calls.
	Credits float64
}

// kiroRealTrafficSessionA: 60 calls. Input ranges 11876..108508 and is NOT
// monotonic -- context compaction and tool-result changes shrink it at
// several points in this series (TestKiroDynamicRealProductionTraffic
// asserts this programmatically rather than pinning specific indices here).
// Output ranges 6..1647 across 57 distinct values.
var kiroRealTrafficSessionA = []kiroRealTrafficRow{
	{Input: 11876, Output: 190, Credits: 0.4309765847},
	{Input: 14547, Output: 128, Credits: 0.3686783171},
	{Input: 15052, Output: 124, Credits: 0.410631679},
	{Input: 18411, Output: 152, Credits: 0.4879462137},
	{Input: 18849, Output: 177, Credits: 0.475745168},
	{Input: 28655, Output: 174, Credits: 0.5306463574},
	{Input: 29073, Output: 225, Credits: 0.5012792269},
	{Input: 37194, Output: 95, Credits: 0.5619823321},
	{Input: 37396, Output: 82, Credits: 0.3118588861},
	{Input: 41598, Output: 71, Credits: 0.4178512738},
	{Input: 41425, Output: 56, Credits: 0.3961602665},
	{Input: 40781, Output: 138, Credits: 0.4633470232},
	{Input: 41865, Output: 87, Credits: 0.3952581775},
	{Input: 44208, Output: 190, Credits: 0.487436114},
	{Input: 46488, Output: 139, Credits: 0.576880445},
	{Input: 44060, Output: 81, Credits: 0.4392530369},
	{Input: 46648, Output: 309, Credits: 0.6670475165},
	{Input: 45321, Output: 81, Credits: 0.3870875034},
	{Input: 45250, Output: 72, Credits: 0.3646204551},
	{Input: 45108, Output: 77, Credits: 0.3269631638},
	{Input: 45572, Output: 93, Credits: 0.3627611413},
	{Input: 45878, Output: 60, Credits: 0.3319594492},
	{Input: 46387, Output: 86, Credits: 0.3607414063},
	{Input: 47408, Output: 275, Credits: 0.5795704835},
	{Input: 47002, Output: 146, Credits: 0.383708284},
	{Input: 47866, Output: 162, Credits: 0.5587370059},
	{Input: 61660, Output: 189, Credits: 0.6675412603},
	{Input: 62447, Output: 76, Credits: 0.6304063534},
	{Input: 63219, Output: 295, Credits: 0.8710134615},
	{Input: 67363, Output: 176, Credits: 0.7632210565},
	{Input: 68227, Output: 553, Credits: 1.0107238198},
	{Input: 67802, Output: 207, Credits: 0.7916248219},
	{Input: 74240, Output: 181, Credits: 0.7807949965},
	{Input: 74289, Output: 94, Credits: 0.7332433303},
	{Input: 70345, Output: 65, Credits: 0.7074256401},
	{Input: 70114, Output: 311, Credits: 1.0561296139},
	{Input: 72647, Output: 89, Credits: 0.7338481063},
	{Input: 73598, Output: 306, Credits: 0.8703871441},
	{Input: 83469, Output: 89, Credits: 0.8246736141},
	{Input: 83846, Output: 6, Credits: 0.6625760924},
	{Input: 84144, Output: 473, Credits: 0.8917576209},
	{Input: 84407, Output: 170, Credits: 0.5879718913},
	{Input: 84455, Output: 187, Credits: 0.6284449833},
	{Input: 84783, Output: 28, Credits: 0.4479463139},
	{Input: 85222, Output: 605, Credits: 1.338492995},
	{Input: 88684, Output: 494, Credits: 1.0193516116},
	{Input: 88783, Output: 591, Credits: 1.1068035555},
	{Input: 98166, Output: 115, Credits: 0.7442763486},
	{Input: 98326, Output: 216, Credits: 0.7005962414},
	{Input: 98569, Output: 450, Credits: 0.9160464111},
	{Input: 102791, Output: 85, Credits: 0.8903650052},
	{Input: 103718, Output: 717, Credits: 1.7068134801},
	{Input: 103999, Output: 709, Credits: 1.1182414616},
	{Input: 104290, Output: 1179, Credits: 1.5334222485},
	{Input: 106233, Output: 26, Credits: 0.7076770384},
	{Input: 106341, Output: 36, Credits: 0.9786186192},
	{Input: 107486, Output: 833, Credits: 1.59400633},
	{Input: 106727, Output: 125, Credits: 0.796781526},
	{Input: 108508, Output: 35, Credits: 0.7273678951},
	{Input: 108045, Output: 1647, Credits: 2.3454465952},
}

// kiroRealTrafficSessionB: 61 calls, concurrent with session A above (same
// account, distinct conversation). Input ranges 9761..146096 and is NOT
// monotonic, with multiple decreases in this series. Output ranges
// 112..1381 across 60 distinct values.
var kiroRealTrafficSessionB = []kiroRealTrafficRow{
	{Input: 9761, Output: 184, Credits: 0.3971629634},
	{Input: 13915, Output: 112, Credits: 0.3365578851},
	{Input: 15232, Output: 188, Credits: 0.4338420128},
	{Input: 15964, Output: 207, Credits: 0.3915734997},
	{Input: 17678, Output: 319, Credits: 0.5162434412},
	{Input: 18026, Output: 361, Credits: 0.5248712246},
	{Input: 18580, Output: 321, Credits: 0.4320238683},
	{Input: 20000, Output: 257, Credits: 0.4960393442},
	{Input: 21597, Output: 753, Credits: 0.8716887871},
	{Input: 26236, Output: 284, Credits: 0.6996584514},
	{Input: 26467, Output: 328, Credits: 0.6960342112},
	{Input: 27697, Output: 449, Credits: 0.8345142444},
	{Input: 38109, Output: 299, Credits: 0.8919056006},
	{Input: 37406, Output: 573, Credits: 1.2161845105},
	{Input: 45117, Output: 392, Credits: 1.0524122491},
	{Input: 45762, Output: 617, Credits: 1.339440725},
	{Input: 49930, Output: 485, Credits: 1.0445462031},
	{Input: 50590, Output: 574, Credits: 1.2413650176},
	{Input: 53674, Output: 279, Credits: 0.9632263021},
	{Input: 53786, Output: 612, Credits: 1.1407139337},
	{Input: 55987, Output: 838, Credits: 1.2711073324},
	{Input: 54805, Output: 702, Credits: 1.0731892184},
	{Input: 55425, Output: 438, Credits: 0.9570720765},
	{Input: 60610, Output: 778, Credits: 1.4663875235},
	{Input: 61608, Output: 679, Credits: 1.5576060385},
	{Input: 63331, Output: 437, Credits: 0.9540809346},
	{Input: 66001, Output: 1381, Credits: 2.1248500238},
	{Input: 66031, Output: 645, Credits: 1.0656958301},
	{Input: 66312, Output: 560, Credits: 1.0916877612},
	{Input: 67310, Output: 262, Credits: 0.7685536485},
	{Input: 67935, Output: 208, Credits: 0.8291650564},
	{Input: 68540, Output: 364, Credits: 0.8875480646},
	{Input: 68273, Output: 276, Credits: 0.7918510217},
	{Input: 69713, Output: 502, Credits: 1.1978694601},
	{Input: 72164, Output: 769, Credits: 1.6218314335},
	{Input: 73579, Output: 635, Credits: 1.4842050415},
	{Input: 78888, Output: 440, Credits: 1.5218671618},
	{Input: 79867, Output: 1150, Credits: 1.9361784955},
	{Input: 80162, Output: 470, Credits: 1.1323307418},
	{Input: 98640, Output: 789, Credits: 1.8980908346},
	{Input: 97782, Output: 456, Credits: 1.8896330171},
	{Input: 105812, Output: 679, Credits: 1.8523032255},
	{Input: 106337, Output: 443, Credits: 1.6595101436},
	{Input: 106816, Output: 775, Credits: 1.4999297465},
	{Input: 118502, Output: 439, Credits: 1.86456187},
	{Input: 118986, Output: 813, Credits: 1.9722874403},
	{Input: 118827, Output: 1065, Credits: 1.5412501203},
	{Input: 121368, Output: 860, Credits: 1.900504633},
	{Input: 128147, Output: 841, Credits: 1.9783194547},
	{Input: 128053, Output: 919, Credits: 2.3405462747},
	{Input: 136733, Output: 416, Credits: 1.9030220146},
	{Input: 136640, Output: 661, Credits: 2.1827924121},
	{Input: 140110, Output: 378, Credits: 1.5919460023},
	{Input: 140653, Output: 525, Credits: 2.5024026611},
	{Input: 142992, Output: 306, Credits: 1.4578299803},
	{Input: 143440, Output: 609, Credits: 1.7262439888},
	{Input: 144481, Output: 370, Credits: 1.4071099274},
	{Input: 143920, Output: 478, Credits: 1.5244380227},
	{Input: 145528, Output: 320, Credits: 1.368700775},
	{Input: 146081, Output: 310, Credits: 1.5099818198},
	{Input: 146096, Output: 658, Credits: 1.7905843033},
}

// kiroRealTrafficTrueRatio replays the same density formula the estimator
// itself uses (see kiroCacheEmulationPlan.complete) directly against the raw
// production triples, anchored on each session's own first (cold, matched==0)
// call, to produce an independent "ground truth" reuse ratio per later call.
// This is NOT part of the estimator and is NOT fed to it -- it exists only so
// tests can check the estimator's output stays conservative relative to what
// the real native-credit series actually implies, the same cross-check that
// found mean ratios of ~0.6697 (session A) / ~0.5174 (session B) and minimums
// of ~0.2425 / ~0.0234 in the raw data underlying this fixture.
func kiroRealTrafficTrueRatio(rows []kiroRealTrafficRow) []float64 {
	ratios := make([]float64, len(rows))
	coldDensity := rows[0].Credits / float64(rows[0].Input+rows[0].Output)
	for i := 1; i < len(rows); i++ {
		density := rows[i].Credits / float64(rows[i].Input+rows[i].Output)
		ratios[i] = (coldDensity - density) / coldDensity
	}
	return ratios
}
