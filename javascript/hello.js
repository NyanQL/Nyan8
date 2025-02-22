
function main(){

    let name = "Nyan8";
    if(typeof allParams.name !== "undefined")
    {
        name = allParams.name;
    }
    return JSON.stringify({
        "success": true,
        "status": 200,
        "data": {
           "message": "hello! " + name
        },
    });
}

main();