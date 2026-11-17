import assert from 'node:assert/strict';
import {modelContext, matchClip, sortClipMatches, compareMatchScores, indexOccurrences, trackGroups,
  assignmentDurationReference, matchesDuration} from '../web/clip-matching.js';

let checks = 0;
const same = (actual, expected, message) => { assert.deepEqual(actual, expected, message); checks++; };
const ids = (prefix, count) => Array.from({length:count}, (_, i) => `${prefix}_${i.toString().padStart(4, '0')}`);
const bodyIds = ids('body', 190), armsIds = ids('arms', 85), gunIds = ids('weapon', 19);
const base = 'C:/Library/Heroes/Example/Skin/Classic';
const model = (guid, boneNames, extra = {}) => ({id:guid, name:guid+'.00C', modelGuid:guid,
  path:`${base}/Models/${guid}.00C/${guid}.owmdl`, bones:boneNames.map(name=>({name})), parentId:null, ...extra});
const body = modelContext(model('ABC001',bodyIds,{entityName:'Gameplay3P'}),`${base}/Entities/Gameplay3P/Gameplay3P.owentity`);
const arms = modelContext(model('ABC002',armsIds,{entityName:'Gameplay1P'}),`${base}/Entities/Gameplay1P/Gameplay1P.owentity`);
const gun = modelContext(model('DEF001',gunIds,{parentId:'ABC002'}),`${base}/Entities/Gameplay1P/Gameplay1P.owentity`);
const clip = (name, boneNames, guid='ABC001', extra={}) => ({name, path:`${base}/Models/${guid}.00C/Animations/0/32/${name}.owanimclip`,
  boneCount:boneNames.length,binding:{boneNames},...extra});
same(body.totalBoneCount,190,'Context uses the actual skeleton length.');
same(body.boneNames instanceof Set,true,'Context retains exact source names.');
same(body.perspective,'Gameplay3P');same(arms.perspective,'Gameplay1P');same(gun.perspective,null,'A weapon child must not acquire the root entity perspective label.');
same(modelContext(model('X',gunIds,{entityName:'Generic'}),'C:/Other/Generic.owentity').perspective,null,'Counts do not infer semantic roles.');
same(modelContext(model('X',gunIds,{entityName:'Gameplay1P'}),'C:/Other/Gameplay3P.owentity').perspective,null,'Conflicting labels stay unknown.');
same(modelContext(null),null);

const partial = clip('partial_112',[...bodyIds.slice(0,108),...ids('other',4)]);
const partialScore = matchClip(partial,body);
same([partialScore.known,partialScore.matched,partialScore.totalBoneCount,partialScore.trackCount,partialScore.extraTracks],
  [true,108,190,112,4],'Partial tracks expose both skeleton and clip denominators.');
same(partialScore.coverage,108/190,'Coverage is of the selected skeleton, not108/112.');
same(partialScore.sameModelFolder,true);same(partialScore.sourceRank,1);
const near = clip('near_count',[...bodyIds.slice(0,186),...ids('other',4)]);
const larger = clip('larger_count',[...bodyIds,...ids('extra',51)]);
same([near.boneCount,larger.boneCount],[190,241]);
same([larger,partial,near].sort((a,b)=>sortClipMatches(a,b,body)).map(c=>c.name),
  ['larger_count','near_count','partial_112'],'More matched object bones outrank closeness to the skeleton track count.');
const gunGood = clip('gun_good',[...gunIds.slice(0,18),'unknown_weapon'],'DEF001');
const gunWrong = clip('other_nineteen',armsIds.slice(0,19),'ABC002');
same([gunGood.boneCount,gunWrong.boneCount],[19,19]);
same([matchClip(gunGood,gun).matched,matchClip(gunWrong,gun).matched],[18,0],'Equal counts do not imply the same joint identities.');
same(matchClip(gunWrong,gun).known,true,'Zero overlap is measured data, not a rejection.');
same(matchClip(gunGood,arms).sameModelFolder,false);

const otherHero = clip('other_hero',bodyIds,'ABC001',{
  path:'C:/Library/Heroes/Different/Skin/Classic/Models/ABC001.00C/Animations/0/32/other_hero.owanimclip'});
const otherSkin = clip('other_skin',bodyIds,'ABC001',{
  path:'C:/Library/Heroes/Example/Skin/Alternative/Models/ABC001.00C/Animations/0/32/other_skin.owanimclip'});
same(matchClip(otherHero,body).matched,190,'Another hero may share all joint names.');
same(matchClip(otherHero,body).sameModelFolder,false,'The same model token under another hero is not source-folder evidence.');
same(matchClip(otherSkin,body).sameModelFolder,false,'The same model token under another skin is a distinct export context.');
same([otherHero,partial].sort((a,b)=>sortClipMatches(a,b,body)).map(c=>c.name),['partial_112','other_hero'],
  'Verified same-object folder outranks incidental joint overlap on another source.');
const caseCopy = {...partial,path:partial.path.replaceAll('/','\\').toUpperCase()};
same(matchClip(caseCopy,body).sameModelFolder,true,'Windows slash and case differences preserve exact source provenance.');
same(matchClip({...partial,path:`${base}/Models/ABC001.00C-backup/Animations/0/32/A.owanimclip`},body).sameModelFolder,false,'Folder boundaries matter.');
same(matchClip({...partial,path:`${base}/Models/ABC001.00C/Textures/A.owanimclip`},body).sameModelFolder,false,'Only the actual model Animations subtree is evidence.');
same(modelContext({...model('ABC001',bodyIds),path:'Models/ABC001.00C/ABC001.owmdl'}).modelFolder,null,'Relative paths cannot establish a shared export root.');
same(modelContext({...model('ABC001',bodyIds),path:'C:/Unstructured/ABC001.owmdl'}).modelFolder,null,'Standalone model files still allow bone matching without guessed folder evidence.');
same(matchClip(partial,modelContext({...model('ABC001',bodyIds),path:'C:/Unstructured/ABC001.owmdl'})).matched,108);

const unknown = {...partial,name:'unknown',binding:undefined};
const failed = {...partial,name:'failed',binding:{error:'Permission denied'}};
same([matchClip(unknown,body).known,matchClip(unknown,body).matched,matchClip(unknown,body).coverage],[false,null,null],'Missing metadata never becomes a false zero.');
same(matchClip(failed,body).error,'Permission denied');
same(matchClip({...partial,error:'Bad file'},body).known,false,'Header/file errors remain unknown.');
same([unknown,otherHero].sort((a,b)=>sortClipMatches(a,b,body)).map(c=>c.name),['other_hero','unknown'],
  'Unknown metadata sorts below known even when its folder matches.');
same(matchClip(partial,null).known,false,'No chosen skeleton has no measured match.');
same(matchClip(partial,modelContext(model('none',[]))).known,false,'A skeletonless object has no match denominator.');
same(matchClip(clip('empty',[]),body).matched,0,'An explicitly empty clip is known zero tracks.');
same(matchClip({...partial,binding:{boneNames:[1,null]}},body).known,false,'Malformed metadata is unknown.');

const representative = {...otherHero,name:'renamed_alias',duplicateKey:'sha256:verified-content',binding:undefined};
const alias = {...partial,duplicateKey:representative.duplicateKey};
const aliasOther = {...partial,name:'same_filename_wrong_bytes',duplicateKey:'sha256:another-payload'};
const rows = [representative,alias,aliasOther], index = indexOccurrences(rows);
const original = JSON.stringify(rows);
for(const occurrences of [rows,index,[alias]]){
  const score = matchClip(representative,body,occurrences);
  same([score.known,score.matched,score.sameModelFolder],[true,108,true],'Byte-identical aliases retain original-folder and binding evidence.');
}
same(matchClip({...representative,duplicateKey:'sha256:no-alias'},body,rows).sameModelFolder,false,'Equal filenames do not borrow evidence.');
same(matchClip({...representative,duplicateError:'Hash unavailable'},body,rows).sameModelFolder,false,'Unverified duplicate groups do not borrow evidence.');
same(matchClip(representative,body,indexOccurrences([{...alias,error:'Unreadable'}])).known,false,'Broken aliases cannot supply unverified metadata.');
same(JSON.stringify(rows),original,'Matching leaves every source row, nickname and binding intact.');
alias.binding={boneNames:bodyIds};
same(matchClip(representative,body,index).matched,190,'Occurrence index references receive later binding updates without reindexing.');
alias.binding=partial.binding;
const minimal = clip('minimal',bodyIds.slice(0,108));
same(compareMatchScores(matchClip(minimal,body),partialScore)<0,true,'Equal matched bone counts prefer fewer extra clip tracks.');
const alpha = clip('alpha',bodyIds), zeta=clip('zeta',bodyIds);
same([zeta,alpha].sort((a,b)=>sortClipMatches(a,b,body)).map(c=>c.name),['alpha','zeta'],'Stable source names break equal scores.');
const cache = new Map([[alpha.path,matchClip(alpha,body)],[zeta.path,matchClip(zeta,body)]]);
same(sortClipMatches(alpha,zeta,null,[],cache),compareMatchScores(cache.get(alpha.path),cache.get(zeta.path)),'Sort accepts precomputed scores for large libraries.');
same(trackGroups([{boneCount:19},{boneCount:112},{boneCount:19},{boneCount:0},{boneCount:-1},{boneCount:1.2},{boneCount:'19'},{boneCount:NaN},{}]),
  [{tracks:112,count:1},{tracks:19,count:2},{tracks:0,count:1}],'Track groups count only actual valid integer headers; callers supply already visible rows.');
same(trackGroups([]),[]);
same(trackGroups([{boneCount:0,error:'Unreadable header'},{boneCount:19,binding:{error:'Unreadable keyframes'}},{boneCount:0}]),
  [{tracks:19,count:1},{tracks:0,count:1}], 'Unreadable headers are excluded, while valid counts remain useful when binding metadata is unavailable.');
same(trackGroups([gunGood,gunWrong]),[{tracks:19,count:2}],'Grouping is exact count only; it does not classify rigs.');

const assignedArms = {name:'ADS Up',durationSeconds:0.5,fps:30};
const assignedAccessory = {name:'Other motion',durationSeconds:3,fps:24};
const assignments = new Map([['arms',assignedArms],['weapon',{durationSeconds:2,fps:60}],['accessory',assignedAccessory]]);
const hierarchy = [{id:'arms'}, {id:'weapon',parentId:'arms'}, {id:'accessory',parentId:'arms'}];
const reference = assignmentDurationReference(assignments,'weapon',hierarchy);
same(reference.clip,assignedArms,'An attached weapon follows its assigned ancestor rather than a later accessory assignment.');
same(assignmentDurationReference(assignments,'arms',hierarchy).clip,assignedAccessory,'Without an assigned ancestor, use the last assigned other object.');
same(assignmentDurationReference(new Map([['weapon',assignedArms]]),'weapon',hierarchy),null,'The selected object never supplies its own suggestion duration.');
same(assignmentDurationReference(new Map(),'weapon',hierarchy),null,'No assignments preserve the original browsing order.');
same(assignmentDurationReference(new Map([['arms',{durationSeconds:0,fps:30}]]),'weapon',hierarchy),null,'Rest poses do not imply a timed companion animation.');
same(assignmentDurationReference(new Map([['arms',{error:'Unreadable',durationSeconds:2,fps:30}]]),'weapon',hierarchy),null,'Invalid assignments do not supply duration evidence.');
same(assignmentDurationReference(assignments,'weapon',[{id:'weapon',parentId:'loop'},{id:'loop',parentId:'weapon'}]).clip,
  assignedAccessory,'Malformed parent cycles terminate and fall back to another assignment.');
same(matchesDuration({lastFrame:30,fps:60},reference),true,'Different FPS compare playback seconds.');
same(matchesDuration({durationFrames:31,fps:60},reference),true,'Frame counts include frame zero when seconds are missing.');
same(matchesDuration({durationSeconds:0.5+1/30,fps:60},reference),true,'One source frame of rounding is allowed across sampling rates.');
same(matchesDuration({durationSeconds:0.5+2/30,fps:60},reference),false,'Two-frame differences do not gain a same-duration preference.');
same(matchesDuration({durationSeconds:0.5},reference),true,'Exact duration remains evidence without FPS.');
same(matchesDuration({durationSeconds:0.51},reference),false,'Missing FPS does not invent a frame tolerance.');
same(matchesDuration({durationSeconds:null,fps:30},reference),false,'Missing duration does not become zero.');
same(matchesDuration({durationSeconds:NaN,fps:30},reference),false,'Non-finite durations cannot match.');
same(matchesDuration({error:'Bad file',durationSeconds:0.5,fps:30},reference),false,'An invalid file gains no duration evidence.');

const longWeapon = clip('A_long',gunIds,'DEF001',{durationSeconds:2,fps:30});
const sameWeapon = clip('Z_same',gunIds,'DEF001',{durationSeconds:0.5,fps:60});
const timedScore = value => matchClip(value,gun,[],reference);
same([longWeapon,sameWeapon].sort((a,b)=>compareMatchScores(timedScore(a),timedScore(b))).map(c=>c.name),
  ['Z_same','A_long'],'Matching companion durations break otherwise equal bone/source scores before filename order.');
same([longWeapon,sameWeapon].sort((a,b)=>sortClipMatches(a,b,gun)).map(c=>c.name),
  ['A_long','Z_same'],'No reference leaves the established sort unchanged.');
same(compareMatchScores(timedScore(longWeapon),timedScore({...sameWeapon,binding:{boneNames:gunIds.slice(0,18)},boneCount:18}))<0,
  true,'More covered joints still outrank a matching duration.');
same(compareMatchScores(timedScore(longWeapon),timedScore({...sameWeapon,binding:{boneNames:[...gunIds,'extra']},boneCount:20}))<0,
  true,'Fewer extra tracks still outrank a matching duration.');
same(compareMatchScores(timedScore(longWeapon),timedScore({...sameWeapon,path:'C:/Other/Weapon.owanimclip'}))<0,
  true,'Source-folder evidence retains its established priority.');
same(compareMatchScores(timedScore(longWeapon),timedScore({...sameWeapon,binding:undefined}))<0,
  true,'Unknown joint metadata does not outrank known matches because of timing.');

// Gameplay3P uses the source model and exact joint overlap together. These
// eight tracks are from the actual Widowmaker 015D first-person clip.
const shoulders = ['bone_000D','bone_0032','bone_0031','bone_0035','bone_004E','bone_004D','bone_0050','bone_007D'];
const legsAndTorso = ['bone_0003','bone_0055','bone_0059','bone_005F','bone_0063'];
const skeleton = [...shoulders, ...legsAndTorso, ...bodyIds.slice(13)];
const character = modelContext(model('011F',skeleton,{entityName:'Gameplay3P'}));
const wrongArms = clip('015D',shoulders,'013E'), bodyOverlay = clip('036B',shoulders,'011F');
const mismatch = (row, related=[]) => matchClip(row,character,related).mismatchReason;
same(matchClip(wrongArms,character).matched,8);
same(Boolean(mismatch(wrongArms)),true,'Matching all eight shoulder tracks cannot identify a foreign clip as a body action.');
same(mismatch(bodyOverlay),'','A sparse clip from the body model stays visible.');
same(mismatch({...wrongArms,path:'C:/Manual/Arms.owanimclip'}),'','Loose partial clips without conflicting evidence stay visible.');
same(Boolean(mismatch(clip('poor loose fit',[...shoulders,...ids('absent',84)],null,{path:'C:/Manual/Unknown.owanimclip'}))),true);
same(mismatch(clip('shared full body',[...shoulders,...legsAndTorso],'013E')),'','Sparse transfers can drive the torso and both legs on a different rig.');
same(mismatch(clip('broad match',skeleton.slice(0,105),'013E')),'','Broadly compatible clips can come from another source model.');
same(mismatch({...bodyOverlay, path:bodyOverlay.path.replace('/Classic/','/Alternate/')}),'','The same model GUID in another skin can supply sparse clips.');
same(Boolean(mismatch(clip('no match',['unbound'],'011F'))),true);
same(mismatch({...wrongArms,binding:undefined}),'','Unread binding metadata is never a measured mismatch.');
same(matchClip(wrongArms,arms).mismatchReason,'','Gameplay1P browsing keeps its own semantics.');
same(matchClip(wrongArms,gun).mismatchReason,'','Body filtering does not apply to attachments.');
const hashedArms = {...wrongArms,duplicateKey:'sha256:001'};
const hashedBody = {...bodyOverlay,duplicateKey:'sha256:001'};
same(mismatch(hashedArms,indexOccurrences([hashedArms,hashedBody])),'','A verified body copy overrides the foreign display path.');
same(Boolean(mismatch(hashedArms,indexOccurrences([hashedArms,{...hashedBody,duplicateError:'hash failed'}]))),true);
same(compareMatchScores(matchClip(clip('usable loose clip',legsAndTorso,null,{path:'C:/Manual/Legs.owanimclip'}),character),matchClip(wrongArms,character))<0,true,'With filtering off, a plausible transfer sorts ahead of a foreign partial.');


console.log(`Clip matching: ${checks} checks passed.`);
